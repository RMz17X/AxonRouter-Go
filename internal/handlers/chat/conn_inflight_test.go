package chat

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"axonrouter/proxy/internal/speedlayer"
)

// installSlotTestStore isolates the process-wide speed layer for one test.
// The chat suite runs serially, so swapping the default store cannot race
// another test's acquire/release.
func installSlotTestStore(t *testing.T) {
	t.Helper()
	previous := speedlayer.Default()
	speedlayer.Install(speedlayer.NewMemoryStore())
	t.Cleanup(func() { speedlayer.Install(previous) })
}

// sharedSlotCount reads the counter through the same store the proxy path
// uses: an in-process cap map in the chat package would be invisible here.
func sharedSlotCount(t *testing.T, connID string) int64 {
	t.Helper()
	counters, err := speedlayer.Default().Counters(context.Background(), connSlotKeyPrefix)
	if err != nil {
		t.Fatalf("read shared slot counters: %v", err)
	}
	return counters[connSlotKey(connID)]
}

func TestTryAcquireConnSlotUsesSharedStore(t *testing.T) {
	installSlotTestStore(t)
	ctx := context.Background()
	const connID = "conn-shared-store"

	if !tryAcquireConnSlot(ctx, connID, antigravityMaxInFlightPerConn) {
		t.Fatal("first acquire was refused with a free slot")
	}
	if got := sharedSlotCount(t, connID); got != 1 {
		t.Fatalf("shared counter after acquire = %d, want 1", got)
	}

	// A second attempt, as if from another replica, must see the same
	// counter and have its failed increment rolled back.
	if tryAcquireConnSlot(ctx, connID, antigravityMaxInFlightPerConn) {
		t.Fatal("second acquire succeeded while the connection was at its cap")
	}
	if got := sharedSlotCount(t, connID); got != 1 {
		t.Fatalf("shared counter after refused acquire = %d, want 1", got)
	}

	releaseConnSlot(connID)
	if got := sharedSlotCount(t, connID); got != 0 {
		t.Fatalf("shared counter after release = %d, want 0", got)
	}
	if !tryAcquireConnSlot(ctx, connID, antigravityMaxInFlightPerConn) {
		t.Fatal("acquire after release was refused")
	}
	releaseConnSlot(connID)
}

func TestTryAcquireConnSlotConcurrentSingleWinner(t *testing.T) {
	installSlotTestStore(t)
	const (
		connID   = "conn-concurrent"
		attempts = 24
	)
	var winners atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if tryAcquireConnSlot(context.Background(), connID, antigravityMaxInFlightPerConn) {
				winners.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := winners.Load(); got != 1 {
		t.Fatalf("concurrent acquire winners = %d, want 1", got)
	}
	if got := sharedSlotCount(t, connID); got != 1 {
		t.Fatalf("shared counter after concurrent acquires = %d, want 1", got)
	}
	releaseConnSlot(connID)
}

type failingCounterStore struct {
	speedlayer.Store
}

func (failingCounterStore) AddCounter(context.Context, string, int64) (int64, error) {
	return 0, errors.New("speed layer unavailable")
}

func TestConnSlotFailsOpenWhenStoreErrors(t *testing.T) {
	previous := speedlayer.Default()
	speedlayer.Install(failingCounterStore{Store: speedlayer.NewMemoryStore()})
	t.Cleanup(func() { speedlayer.Install(previous) })

	if !tryAcquireConnSlot(context.Background(), "conn-failopen", antigravityMaxInFlightPerConn) {
		t.Fatal("unreachable speed layer must not refuse an attempt")
	}
	// The caller defers release on every path, including this one.
	releaseConnSlot("conn-failopen")
}
