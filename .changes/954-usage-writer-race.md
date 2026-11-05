## Usage writer: the data race is gone

**Symptom.** CI's race-detector job reported a data race in the chat
package and a failing assertion in
TestUsageWriter_FlushWithoutWorker ("flush waited on a queue that was
never created").

**Root cause.** Two halves of one design fault. The worker loop re-read
the package-level queue variables in its select on every iteration, while
TestUsageWriter_FlushWithoutWorker reset those variables (and the Once)
without synchronisation to fake a cold process — a write racing the
loop's reads for its whole lifetime. The reset also never cleared
usageWorkerReady, so in a suite process where another test had already
started the worker, the drain waited on the nilled queue and the test
failed on its own premise.

**Fix.** usageWorkerLoop now receives its two queues as parameters,
captured when ensureUsageWorker starts it — the loop never re-reads the
package variables (the same rule FlushUsageWrites already followed via
usageWorkerReady). The test no longer mutates shared state: it pins the
idle-drain contract (an idle flush reports success promptly), which both
the cold and warm process states satisfy.

**Verification.** `go test -race -run TestUsageWriter
./internal/handlers/chat/` passes; previously the race detector flagged
the test/loop pair and the assertion failed in suite order.
