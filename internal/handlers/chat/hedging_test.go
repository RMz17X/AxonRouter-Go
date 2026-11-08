package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"axonrouter/proxy/internal/models"
)

func TestHedgeCoordinator_ClaimWinOnce(t *testing.T) {
	coord := newHedgeCoordinator(2)

	var cand0Canceled, cand1Canceled atomic.Bool
	ctx0, cancel0 := context.WithCancel(context.Background())
	ctx1, cancel1 := context.WithCancel(context.Background())

	go func() {
		<-ctx0.Done()
		cand0Canceled.Store(true)
	}()
	go func() {
		<-ctx1.Done()
		cand1Canceled.Store(true)
	}()

	coord.setCancel(0, cancel0)
	coord.setCancel(1, cancel1)

	// Candidate 0 claims win
	if !coord.claimWin(0) {
		t.Fatal("expected candidate 0 to claim win")
	}

	// Candidate 1 tries to claim win after candidate 0 already won
	if coord.claimWin(1) {
		t.Fatal("expected candidate 1 to fail claiming win after candidate 0 already won")
	}

	// Wait for context cancellation
	time.Sleep(20 * time.Millisecond)

	if cand0Canceled.Load() {
		t.Error("winner candidate 0 context should NOT be canceled")
	}
	if !cand1Canceled.Load() {
		t.Error("losing candidate 1 context MUST be canceled immediately")
	}
	if coord.getWinner() != 0 {
		t.Errorf("expected winner 0, got %d", coord.getWinner())
	}
}

func TestHedgedResponseWriter_WinnerCommitsAndStreams(t *testing.T) {
	rec := httptest.NewRecorder()
	coord := newHedgeCoordinator(2)

	hw0 := newHedgedResponseWriter(rec, coord, 0)
	hw1 := newHedgedResponseWriter(rec, coord, 1)

	hw0.Header().Set("X-Candidate", "0")
	hw0.Header().Set("Content-Type", "text/event-stream")

	hw1.Header().Set("X-Candidate", "1")

	// Candidate 0 writes 200 OK and SSE chunks
	hw0.WriteHeader(http.StatusOK)
	_, _ = hw0.Write([]byte("data: hello\n\n"))

	// Candidate 1 writes data (loser)
	hw1.WriteHeader(http.StatusOK)
	_, _ = hw1.Write([]byte("data: SHOULD NOT BE IN MASTER\n\n"))

	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", res.StatusCode)
	}
	if res.Header.Get("X-Candidate") != "0" {
		t.Errorf("expected header from candidate 0, got %s", res.Header.Get("X-Candidate"))
	}
	bodyStr := rec.Body.String()
	if bodyStr != "data: hello\n\n" {
		t.Errorf("expected master body to contain only candidate 0 chunks, got: %s", bodyStr)
	}
}

func TestShouldHedgeProvider_DefaultDahl(t *testing.T) {
	h := &ChatHandler{}

	enabled, delay, maxAttempts := h.shouldHedgeProvider("dahl")
	if !enabled {
		t.Error("expected dahl to have hedging enabled by default")
	}
	if delay != 120*time.Millisecond {
		t.Errorf("expected default 120ms delay, got %v", delay)
	}
	if maxAttempts != 2 {
		t.Errorf("expected default 2 max attempts, got %d", maxAttempts)
	}

	otherEnabled, _, _ := h.shouldHedgeProvider("openai")
	if otherEnabled {
		t.Error("expected other providers to not have hedging enabled by default")
	}
}

func TestPickHedgeCandidates(t *testing.T) {
	nameA := "Account A"
	nameB := "Account B"
	nameC := "Account C"
	allConns := []*models.ProviderConnection{
		{ID: "c1", Provider: "dahl", Name: &nameA, IsActive: 1},
		{ID: "c2", Provider: "dahl", Name: &nameB, IsActive: 1},
		{ID: "c3", Provider: "dahl", Name: &nameC, IsActive: 0}, // inactive
	}

	h := &ChatHandler{}
	picked := h.pickHedgeCandidates(allConns, "dahl", "deepseek-v4-flash", 2)
	if len(picked) != 2 {
		t.Fatalf("expected 2 active candidates, got %d", len(picked))
	}
	if picked[0].ID != "c1" || picked[1].ID != "c2" {
		t.Errorf("unexpected candidates picked: %v, %v", picked[0].ID, picked[1].ID)
	}
}
