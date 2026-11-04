package chat

import (
	"sync"
	"sync/atomic"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
)

// Off-path usage writes.
//
// logUsage used to run every database write it needed — the usageHistory
// row, the requestDetails row, the compression-analytics row, and the
// daily aggregate read-modify-write — on the goroutine that had just
// finished streaming the response. All of them shared one WAL writer, so
// under load each completion waited behind every other completion and
// behind the dashboard's own writes; and the daily blob RMW alone costs
// ~21 ms once a busy day has grown it past 400 KB, paid per request on a
// global mutex.
//
// None of that work changes what the client sees on the wire, so it runs
// on a single background worker instead. The worker drains TWO lanes:
//
//   - The LEDGER lane (usageHistory + requestDetails inserts) carries
//     the billing truth. It is never droppable: enqueueing blocks
//     (bounded) until the worker takes the record, and the worker lands
//     records as multi-row batch INSERTs, so drain throughput exceeds
//     burst arrival by orders of magnitude. Review cycle 1 (F2) proved
//     the previous single droppable queue discarded ~5% of ledger rows
//     at 2,000-request scale — the two ledger tables even diverged from
//     each other, because drops hit them independently.
//   - The GENERIC lane keeps the old bounded, drop-on-full contract for
//     work that is allowed to be lost under overload — today that is
//     compression analytics, a telemetry mirror of the ledger, not the
//     ledger itself.
//
// The daily aggregate rides neither lane: it accumulates in memory and
// flushes as one read-modify-write per interval (see usage_daily.go).
// It is an aggregate OVER the ledger, so the ledger landing first and
// the aggregate following seconds later loses nothing an operator can
// observe.

// usageWriteQueueDepth bounds the pending generic jobs. At the ~20 KB a
// payload can reach, a full queue is a few MB of retained payloads at
// worst.
const usageWriteQueueDepth = 256

// ledgerWriteQueueDepth bounds the pending ledger records. Ledger
// records are small (a requestDetails payload dominates at ~20 KB), and
// the queue drains in batches, so this depth is a burst absorber, not a
// steady-state backlog.
const ledgerWriteQueueDepth = 512

// ledgerBatchMax is the row count at which the worker flushes a pending
// ledger batch immediately instead of waiting for the flush timer.
const ledgerBatchMax = 128

// ledgerFlushInterval bounds how long a ledger record may sit in the
// worker's pending batch before it lands. It is a latency floor for
// dashboard freshness, not a throughput knob: batches under load flush
// on size long before the timer matters.
const ledgerFlushInterval = 25 * time.Millisecond

// ledgerEnqueueTimeout bounds how long a completion goroutine may wait
// for room in the ledger lane. The worker drains continuously while the
// database is reachable, so reaching the timeout means the database is
// wedged; at that point the record is dropped and counted rather than
// parking response goroutines behind a dead ledger — and the drop is
// visible in GET /api/system/memory, not only in the logs.
const ledgerEnqueueTimeout = 10 * time.Second

// usageWriteJob is one self-contained unit of generic-lane persistence.
// It captures the repo and the already-built payloads; the worker runs
// it verbatim.
type usageWriteJob func()

// ledgerWrite is one ledger-lane unit: exactly one of history/detail is
// set, or barrier is set. A barrier is the flush marker FlushUsageWrites
// sends: the worker lands every pending batch before closing it, which
// is what makes "enqueued before the call" observable.
type ledgerWrite struct {
	repo    *db.Repo
	history *db.UsageHistoryRecord
	detail  *db.RequestDetailRecord
	barrier chan struct{}
}

var (
	usageWriteOnce     sync.Once
	usageWriteQueue    chan usageWriteJob
	ledgerWriteQueue   chan ledgerWrite
	usageWritesDropped atomic.Int64
	// usageWorkerReady is set inside the Once, before the worker starts,
	// so a reader that observes it also observes the queues (the atomic
	// store/load pair is the happens-before edge). FlushUsageWrites
	// consults it instead of reading the queue variables directly, which
	// raced the worker's lazy start (review cycle 2, N8).
	usageWorkerReady atomic.Bool
	// usageSynchronous runs every job inline at enqueue time. The integration
	// harness sets it: its assertions read usage rows straight after the HTTP
	// response, and teaching every one of them to drain would hide the exact
	// ordering the production path guarantees anyway.
	usageSynchronous atomic.Bool
)

func ensureUsageWorker() {
	usageWriteOnce.Do(func() {
		usageWriteQueue = make(chan usageWriteJob, usageWriteQueueDepth)
		ledgerWriteQueue = make(chan ledgerWrite, ledgerWriteQueueDepth)
		usageWorkerReady.Store(true)
		go usageWorkerLoop()
	})
}

// usageWorkerLoop is the single drain for both lanes. It owns the
// pending ledger batches; only this goroutine touches them.
func usageWorkerLoop() {
	var pendingHistory []ledgerWrite
	var pendingDetails []ledgerWrite

	var timer *time.Timer
	var timerC <-chan time.Time
	armTimer := func() {
		if timer != nil {
			return
		}
		timer = time.NewTimer(ledgerFlushInterval)
		timerC = timer.C
	}
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
			timerC = nil
		}
	}

	flushPending := func() {
		stopTimer()
		if len(pendingHistory) > 0 {
			flushLedgerBatch(pendingHistory, true)
			pendingHistory = nil
		}
		if len(pendingDetails) > 0 {
			flushLedgerBatch(pendingDetails, false)
			pendingDetails = nil
		}
	}

	for {
		select {
		case job := <-usageWriteQueue:
			job()
		case lw := <-ledgerWriteQueue:
			switch {
			case lw.barrier != nil:
				flushPending()
				close(lw.barrier)
			case lw.history != nil:
				pendingHistory = append(pendingHistory, lw)
				if len(pendingHistory) >= ledgerBatchMax {
					flushPending()
				} else {
					armTimer()
				}
			case lw.detail != nil:
				pendingDetails = append(pendingDetails, lw)
				if len(pendingDetails) >= ledgerBatchMax {
					flushPending()
				} else {
					armTimer()
				}
			}
		case <-timerC:
			timer = nil
			timerC = nil
			flushPending()
		}
	}
}

// flushLedgerBatch lands one pending run as batch INSERTs. Records are
// grouped by consecutive repo (in practice the process has exactly one
// repo; the grouping keeps a multi-repo test honest without a sort).
func flushLedgerBatch(pending []ledgerWrite, history bool) {
	for start := 0; start < len(pending); {
		repo := pending[start].repo
		end := start + 1
		for end < len(pending) && pending[end].repo == repo {
			end++
		}
		if repo != nil {
			if history {
				recs := make([]db.UsageHistoryRecord, 0, end-start)
				for _, lw := range pending[start:end] {
					recs = append(recs, *lw.history)
				}
				insertLedgerHistoryBatch(repo, recs)
			} else {
				recs := make([]db.RequestDetailRecord, 0, end-start)
				for _, lw := range pending[start:end] {
					recs = append(recs, *lw.detail)
				}
				insertLedgerDetailBatch(repo, recs)
			}
		}
		start = end
	}
}

// insertLedgerHistoryBatch lands one run of history records. A failed
// batch statement falls back to per-row inserts: batching must not raise
// the blast radius of one bad record from one row to the whole run
// (review cycle 2, N1). A worker run never exceeds one of the batch
// writer's internal chunks (ledgerBatchMax < usageInsertChunk), so the
// batch statement is atomic and the fallback can never re-land a row
// the batch already wrote. Rows that still fail per-row are LOST, and
// are counted in usageWritesDropped — the counter an operator reads in
// GET /api/system/memory must mean all ledger loss, not only the
// queue-full kind. The fallback runs only on the failure path; the
// happy path stays one statement per run.
func insertLedgerHistoryBatch(repo *db.Repo, recs []db.UsageHistoryRecord) {
	if err := repo.InsertUsageHistoryBatch(recs); err != nil {
		log.Error("usage", "insert usage history batch failed", "rows", len(recs), "error", err)
		lost := 0
		for i := range recs {
			if err := repo.InsertUsageHistoryBatch(recs[i : i+1]); err != nil {
				lost++
				log.Error("usage", "insert usage history row failed", "error", err)
			}
		}
		if lost > 0 {
			dropped := usageWritesDropped.Add(int64(lost))
			log.Error("usage", "usage history rows lost after per-row retry", "rows", lost, "dropped_total", dropped)
		}
	}
}

// insertLedgerDetailBatch is insertLedgerHistoryBatch for request
// details. The batch writer's ON CONFLICT DO NOTHING grain is preserved
// per row: a conflicting id is a dedupe, not a loss, and never reaches
// the counter.
func insertLedgerDetailBatch(repo *db.Repo, recs []db.RequestDetailRecord) {
	if err := repo.InsertRequestDetailBatch(recs); err != nil {
		log.Error("usage", "insert request detail batch failed", "rows", len(recs), "error", err)
		lost := 0
		for i := range recs {
			if err := repo.InsertRequestDetailBatch(recs[i : i+1]); err != nil {
				lost++
				log.Error("usage", "insert request detail row failed", "error", err)
			}
		}
		if lost > 0 {
			dropped := usageWritesDropped.Add(int64(lost))
			log.Error("usage", "request detail rows lost after per-row retry", "rows", lost, "dropped_total", dropped)
		}
	}
}

// enqueueUsageWrite persists one generic-lane unit off the response
// goroutine. A full queue drops the job: the warning carries the running
// total so an operator can tell a one-off burst from a sustained
// backlog. Only droppable work may ride this lane — ledger rows go
// through enqueueLedgerWrite.
func enqueueUsageWrite(job usageWriteJob) {
	if job == nil {
		return
	}
	if usageSynchronous.Load() {
		job()
		return
	}
	ensureUsageWorker()
	select {
	case usageWriteQueue <- job:
	default:
		dropped := usageWritesDropped.Add(1)
		log.Warn("usage", "write queue full, dropping usage row", "dropped_total", dropped)
	}
}

// enqueueLedgerWrite persists one ledger record off the response
// goroutine. Unlike the generic lane it waits for room: the ledger is
// the billing truth, so backpressure belongs here rather than silent
// loss. The wait is bounded by ledgerEnqueueTimeout (see its comment).
func enqueueLedgerWrite(lw ledgerWrite) {
	if lw.history == nil && lw.detail == nil {
		return
	}
	if usageSynchronous.Load() {
		flushLedgerBatch([]ledgerWrite{lw}, lw.history != nil)
		return
	}
	ensureUsageWorker()
	timer := time.NewTimer(ledgerEnqueueTimeout)
	defer timer.Stop()
	select {
	case ledgerWriteQueue <- lw:
	case <-timer.C:
		dropped := usageWritesDropped.Add(1)
		log.Warn("usage", "ledger write queue full, dropping usage row", "dropped_total", dropped)
	}
}

// FlushUsageWrites waits until every usage write enqueued before the
// call has landed, or the timeout expires. It is the drain hook for
// graceful shutdown and for tests that opt into the asynchronous path.
// Reports whether the drain completed.
func FlushUsageWrites(timeout time.Duration) bool {
	if usageSynchronous.Load() {
		return true
	}
	// A gateway that never served a request has no worker and nothing to
	// drain. The check goes through the Once-guarded ready flag the
	// enqueue paths establish, not the queue variables themselves
	// (review cycle 2, N8).
	if !usageWorkerReady.Load() {
		return true
	}
	deadline := time.Now().Add(timeout)
	genericDone := make(chan struct{})
	select {
	case usageWriteQueue <- func() { close(genericDone) }:
	case <-time.After(timeout):
		return false
	}
	ledgerDone := make(chan struct{})
	select {
	case ledgerWriteQueue <- ledgerWrite{barrier: ledgerDone}:
	case <-time.After(time.Until(deadline)):
		return false
	}
	for _, done := range []chan struct{}{genericDone, ledgerDone} {
		select {
		case <-done:
		case <-time.After(time.Until(deadline)):
			return false
		}
	}
	// The daily accumulator is part of "every usage write", but it
	// summarises the ledger, so it flushes AFTER the ledger barrier:
	// every delta it folds now corresponds to ledger rows that have
	// landed. Flushing it on this goroutine also means a shutdown does
	// not wait out its ticker.
	flushDailyUsage()
	return true
}

// SetUsageWritesSynchronousForTest toggles inline execution. The integration
// harness turns it on at boot; the async-specific tests turn it off and flush
// explicitly.
func SetUsageWritesSynchronousForTest(sync bool) {
	usageSynchronous.Store(sync)
}

// UsageWritesDropped reports how many usage writes were lost since
// process start: generic-lane jobs dropped at a full queue, ledger
// records dropped after their bounded enqueue wait, and ledger rows
// whose insert failed even on the per-row retry (review cycle 2, N1).
// It is surfaced in GET /api/system/memory: ledger loss must be
// operator-visible, not buried in warning logs.
func UsageWritesDropped() int64 {
	return usageWritesDropped.Load()
}
