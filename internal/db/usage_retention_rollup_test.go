package db_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	json "encoding/json/v2"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/db/dbtest"
)

// The retention prune is a DELETION: every row it removes is gone from the
// ledger, and with it every number the dashboard derives from that row. The
// daily rollup (usageDaily) is what keeps those numbers alive — so the prune
// must roll the doomed rows up BEFORE it deletes them.
//
// These tests run against a real PostgreSQL (internal/db/dbtest provisions a
// throwaway schema inside the database INTEGRATION_DATABASE_URL names), not a
// mock: the production schema partitions usageHistory monthly and stores
// usageDaily.data as JSONB, and a mock would exercise neither.
//
// The suite lives in the external db_test package because the provisioning
// helper (internal/db/dbtest) imports internal/db — an in-package test file
// could not import it without an import cycle.

// pgRetentionRepo returns a repo whose tables come from the real schema
// bootstrap, so the tests exercise the indexes, the partitioning and the
// column types production uses rather than a hand-built fixture.
//
// When no database is configured the suite skips (owner rule, 2026-11-06): a
// test that needs a database is not a failure when CI provisions none.
func pgRetentionRepo(t *testing.T) *db.Repo {
	t.Helper()
	conn, _, cleanupDB, err := dbtest.NewSchemaHandle("rollup")
	if err != nil {
		if errors.Is(err, dbtest.ErrNoDatabase) {
			t.Skipf("skipping database-backed test: %v", err)
		}
		t.Fatalf("provision test database: %v", err)
	}
	t.Cleanup(cleanupDB)
	if err := db.EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	return db.NewRepo(conn)
}

// pgCutoff is the exclusive upper bound a 30-day prune uses, spelled the way
// the ledger spells it (RFC3339, UTC, second precision).
func pgCutoff(days int) string {
	return time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
}

// pgCountRows counts the rows of one table.
func pgCountRows(t *testing.T, repo *db.Repo, table string) int {
	t.Helper()
	var n int
	if err := repo.RawDB().QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// pgEnsurePartition creates the monthly partition that covers the given day
// when the bootstrap did not create it. The production bootstrap only creates
// the current and next month's partitions, so a test that dates a row into an
// older month has to create that month first — otherwise the insert has no
// partition to land in.
func pgEnsurePartition(t *testing.T, repo *db.Repo, day time.Time) {
	t.Helper()
	from := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	suffix := fmt.Sprintf("y%04dm%02d", from.Year(), int(from.Month()))
	bounds := fmt.Sprintf("FOR VALUES FROM ('%04d-%02d-01') TO ('%04d-%02d-01')",
		from.Year(), int(from.Month()), to.Year(), int(to.Month()))
	for _, parent := range []string{"usagehistory", "requestdetails"} {
		ddl := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s_%s PARTITION OF %s %s",
			parent, suffix, parent, bounds)
		if _, err := repo.RawDB().Exec(ddl); err != nil {
			t.Fatalf("create partition %s_%s: %v", parent, suffix, err)
		}
	}
}

// pgSeedLedgerRow inserts one usageHistory row with exact details.
//
// The timestamp is written as UTC RFC3339 — the format the ledger writer
// stamps — and the apiKey is already masked, the way the writer stores it.
// The statement is authored in PostgreSQL's native $n form because it runs on
// the raw handle, which does not rebind '?' placeholders (only the Repo
// wrappers do).
func pgSeedLedgerRow(t *testing.T, repo *db.Repo, at time.Time, provider, model, connID, apiKey, endpoint, status, tokensJSON string, prompt, compl int, cost float64) {
	t.Helper()
	ts := at.UTC().Truncate(time.Second).Format(time.RFC3339)
	if _, err := repo.RawDB().Exec(`
		INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, clientKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, '{}'::jsonb)`,
		ts, provider, model, connID, apiKey, apiKey, endpoint, prompt, compl, cost, status, tokensJSON); err != nil {
		t.Fatalf("seed usageHistory %s: %v", ts, err)
	}
}

// pgDaily reads one usageDaily row's payload.
func pgDaily(t *testing.T, repo *db.Repo, dateKey string) map[string]any {
	t.Helper()
	raw, err := repo.GetUsageDaily(dateKey)
	if err != nil {
		t.Fatalf("GetUsageDaily %s: %v", dateKey, err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal daily %s: %v", dateKey, err)
	}
	return m
}

// pgDailyInt reads one numeric field out of a daily payload.
func pgDailyInt(t *testing.T, m map[string]any, key string) int {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("daily field %q = %#v, want a number", key, m[key])
	}
	return int(v)
}

// TestPruneUsageHistory_RollsUpBeforeDeleting is the P0 proof: a ledger row
// that the prune removes lands in usageDaily with the right totals, and the
// raw row is gone afterwards. Before the rollup landed (upstream #1056), the
// row was deleted with nothing written to usageDaily and the day's numbers
// disappeared from analytics for good.
func TestPruneUsageHistory_RollsUpBeforeDeleting(t *testing.T) {
	repo := pgRetentionRepo(t)
	ctx := context.Background()

	// The two doomed days sit two months back, so they are inside the
	// retention window but outside the partitions the bootstrap created.
	day1 := time.Now().UTC().AddDate(0, -2, 0).Truncate(24 * time.Hour)
	day2 := day1.AddDate(0, 0, 1)
	pgEnsurePartition(t, repo, day1)

	// Row 1 on day1: openai / gpt-4o, 100 prompt + 50 compl, 20 cached, cost 0.002
	pgSeedLedgerRow(t, repo, day1.Add(10*time.Hour), "openai", "gpt-4o", "conn-1",
		"sk-test-key-abcdefgh", "/v1/chat/completions", "success",
		`{"prompt_tokens":100,"completion_tokens":50,"cached_tokens":20}`, 100, 50, 0.002)
	// Row 2 on day1: anthropic / claude, 200 prompt + 100 compl, 0 cached, cost 0.005
	pgSeedLedgerRow(t, repo, day1.Add(11*time.Hour), "anthropic", "claude-3-5-sonnet", "conn-2",
		"sk-test-key-abcdefgh", "/v1/chat/completions", "success",
		`{"prompt_tokens":200,"completion_tokens":100,"cached_tokens":0}`, 200, 100, 0.005)
	// Row 3 on day2: openai / gpt-4o, 300 prompt + 150 compl, 50 cached, cost 0.006
	pgSeedLedgerRow(t, repo, day2.Add(10*time.Hour), "openai", "gpt-4o", "conn-1",
		"sk-test-key-abcdefgh", "/v1/chat/completions", "success",
		`{"prompt_tokens":300,"completion_tokens":150,"cached_tokens":50}`, 300, 150, 0.006)
	// Row 4 is recent: it must survive the prune untouched.
	recent := time.Now().UTC().Add(-2 * time.Hour)
	pgSeedLedgerRow(t, repo, recent, "openai", "gpt-4o", "conn-1",
		"sk-test-key-abcdefgh", "/v1/chat/completions", "success", `{}`, 50, 25, 0.001)

	day1Key := day1.In(time.Local).Format("2006-01-02")
	day2Key := day2.In(time.Local).Format("2006-01-02")

	// No daily summary exists yet: the live path never saw these rows.
	if _, err := repo.GetUsageDaily(day1Key); err == nil {
		t.Fatalf("expected no daily usage for %s before the prune", day1Key)
	}

	res, err := repo.PruneUsageHistory(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("PruneUsageHistory: %v", err)
	}
	if res.Rows != 3 {
		t.Errorf("pruned rows = %d, want 3", res.Rows)
	}

	// The rollup landed: day1 carries both of its rows.
	d1 := pgDaily(t, repo, day1Key)
	if got := pgDailyInt(t, d1, "requests"); got != 2 {
		t.Errorf("%s requests = %d, want 2", day1Key, got)
	}
	if got := pgDailyInt(t, d1, "promptTokens"); got != 300 {
		t.Errorf("%s promptTokens = %d, want 300", day1Key, got)
	}
	if got := pgDailyInt(t, d1, "completionTokens"); got != 150 {
		t.Errorf("%s completionTokens = %d, want 150", day1Key, got)
	}
	if got := pgDailyInt(t, d1, "cachedTokens"); got != 20 {
		t.Errorf("%s cachedTokens = %d, want 20", day1Key, got)
	}
	if got, ok := d1["cost"].(float64); !ok || got < 0.0069 || got > 0.0071 {
		t.Errorf("%s cost = %v, want ~0.007", day1Key, d1["cost"])
	}
	if got := pgDailyInt(t, d1, "totalRequests"); got != 2 {
		t.Errorf("%s totalRequests = %d, want 2", day1Key, got)
	}

	// Day2 carries its single row.
	d2 := pgDaily(t, repo, day2Key)
	if got := pgDailyInt(t, d2, "requests"); got != 1 {
		t.Errorf("%s requests = %d, want 1", day2Key, got)
	}
	if got := pgDailyInt(t, d2, "promptTokens"); got != 300 {
		t.Errorf("%s promptTokens = %d, want 300", day2Key, got)
	}
	if got := pgDailyInt(t, d2, "cachedTokens"); got != 50 {
		t.Errorf("%s cachedTokens = %d, want 50", day2Key, got)
	}

	// The raw rows are gone; only the recent one survived.
	if got := pgCountRows(t, repo, "usageHistory"); got != 1 {
		t.Errorf("surviving usageHistory rows = %d, want 1", got)
	}
}

// TestPruneUsageHistory_RollupIsIdempotent proves the rollup cannot double
// count. The live request path writes usageDaily in real time, so a day that
// already has a summary must NOT absorb the raw rows again — and a day that
// has no summary must not absorb them twice, however often the prune runs.
//
// The second half is the partial-prune case: a prune that deleted some rows
// before the process died leaves the day's summary in place, so the next
// prune must not add the survivors on top of it.
func TestPruneUsageHistory_RollupIsIdempotent(t *testing.T) {
	repo := pgRetentionRepo(t)
	ctx := context.Background()

	day := time.Now().UTC().AddDate(0, -2, 0).Truncate(24 * time.Hour)
	pgEnsurePartition(t, repo, day)
	dayKey := day.In(time.Local).Format("2006-01-02")

	pgSeedLedgerRow(t, repo, day.Add(10*time.Hour), "openai", "gpt-4o", "conn-1",
		"sk-test-key-abcdefgh", "/v1/chat/completions", "success",
		`{"prompt_tokens":100,"completion_tokens":50}`, 100, 50, 0.002)
	pgSeedLedgerRow(t, repo, day.Add(11*time.Hour), "anthropic", "claude-3-5-sonnet", "conn-2",
		"sk-test-key-abcdefgh", "/v1/chat/completions", "success",
		`{"prompt_tokens":200,"completion_tokens":100}`, 200, 100, 0.005)

	// The live path already summarised this day.
	live := `{"requests":5,"promptTokens":100,"completionTokens":40,"cachedTokens":10,"cost":0.01,"totalRequests":5}`
	if err := repo.UpsertUsageDaily(dayKey, live); err != nil {
		t.Fatalf("UpsertUsageDaily: %v", err)
	}

	// A prune must leave the live summary exactly as it found it.
	if _, err := repo.PruneUsageHistory(ctx, 30*24*time.Hour); err != nil {
		t.Fatalf("PruneUsageHistory: %v", err)
	}
	d := pgDaily(t, repo, dayKey)
	if got := pgDailyInt(t, d, "requests"); got != 5 {
		t.Errorf("requests = %d, want 5: the rollup must not double count an existing day", got)
	}
	if got := pgDailyInt(t, d, "promptTokens"); got != 100 {
		t.Errorf("promptTokens = %d, want 100: the rollup must not double count an existing day", got)
	}

	// A day with NO summary absorbs its rows exactly once, no matter how many
	// times the rollup runs. The second run finds the summary the first run
	// wrote and must not add to it.
	day2 := day.AddDate(0, 0, 1)
	pgEnsurePartition(t, repo, day2)
	day2Key := day2.In(time.Local).Format("2006-01-02")
	pgSeedLedgerRow(t, repo, day2.Add(10*time.Hour), "openai", "gpt-4o", "conn-1",
		"sk-test-key-abcdefgh", "/v1/chat/completions", "success",
		`{"prompt_tokens":300,"completion_tokens":150}`, 300, 150, 0.006)

	n, err := repo.RollupUsageHistoryBeforeDate(ctx, pgCutoff(30))
	if err != nil {
		t.Fatalf("RollupUsageHistoryBeforeDate: %v", err)
	}
	if n != 1 {
		t.Errorf("rolled up dates = %d, want 1 (only the day with no summary)", n)
	}
	d2 := pgDaily(t, repo, day2Key)
	if got := pgDailyInt(t, d2, "requests"); got != 1 {
		t.Fatalf("%s requests = %d, want 1", day2Key, got)
	}

	// Second run over the same rows: the summary now exists, so nothing is
	// added.
	n, err = repo.RollupUsageHistoryBeforeDate(ctx, pgCutoff(30))
	if err != nil {
		t.Fatalf("second RollupUsageHistoryBeforeDate: %v", err)
	}
	if n != 0 {
		t.Errorf("second rollup dates = %d, want 0: the day already has a summary", n)
	}
	d2again := pgDaily(t, repo, day2Key)
	if got := pgDailyInt(t, d2again, "requests"); got != 1 {
		t.Errorf("%s requests after second rollup = %d, want 1: the rollup double counted", day2Key, got)
	}
	if got := pgDailyInt(t, d2again, "promptTokens"); got != 300 {
		t.Errorf("%s promptTokens after second rollup = %d, want 300: the rollup double counted", day2Key, got)
	}
}

// TestPruneUsageHistory_RollupCountsFailedRequests proves the rollup folds a
// failed ledger row as an error (requests + errors, no token movement) rather
// than as a success, so the day's error count survives the prune.
func TestPruneUsageHistory_RollupCountsFailedRequests(t *testing.T) {
	repo := pgRetentionRepo(t)
	ctx := context.Background()

	day := time.Now().UTC().AddDate(0, -2, 0).Truncate(24 * time.Hour)
	pgEnsurePartition(t, repo, day)
	dayKey := day.In(time.Local).Format("2006-01-02")

	pgSeedLedgerRow(t, repo, day.Add(10*time.Hour), "openai", "gpt-4o", "conn-1",
		"sk-test-key-abcdefgh", "/v1/chat/completions", "success",
		`{"prompt_tokens":100,"completion_tokens":50}`, 100, 50, 0.002)
	pgSeedLedgerRow(t, repo, day.Add(11*time.Hour), "openai", "gpt-4o", "conn-1",
		"sk-test-key-abcdefgh", "/v1/chat/completions", "error", `{}`, 0, 0, 0)

	if _, err := repo.PruneUsageHistory(ctx, 30*24*time.Hour); err != nil {
		t.Fatalf("PruneUsageHistory: %v", err)
	}
	d := pgDaily(t, repo, dayKey)
	if got := pgDailyInt(t, d, "requests"); got != 2 {
		t.Errorf("%s requests = %d, want 2", dayKey, got)
	}
	if got := pgDailyInt(t, d, "errors"); got != 1 {
		t.Errorf("%s errors = %d, want 1", dayKey, got)
	}
	if got := pgDailyInt(t, d, "promptTokens"); got != 100 {
		t.Errorf("%s promptTokens = %d, want 100: the failed row must not add tokens", dayKey, got)
	}
}

// TestRollupUsageHistoryBeforeDate_KeepsLocalDayAnchor proves the rollup keys
// its days the way the live writer keys them: the process-local calendar day,
// not UTC. A row stamped late in the UTC day belongs to the next local day
// when the process runs east of UTC, and the rollup must put it in the same
// bucket the live writer would have — otherwise one day's traffic is split
// across two date keys and the chart draws two half-days.
func TestRollupUsageHistoryBeforeDate_KeepsLocalDayAnchor(t *testing.T) {
	repo := pgRetentionRepo(t)
	ctx := context.Background()

	// A row stamped 23:30 UTC two months back. East of UTC that is the NEXT
	// local day.
	stamp := time.Now().UTC().AddDate(0, -2, 0).Truncate(24 * time.Hour).Add(23*time.Hour + 30*time.Minute)
	pgEnsurePartition(t, repo, stamp)

	pgSeedLedgerRow(t, repo, stamp, "openai", "gpt-4o", "conn-1",
		"sk-test-key-abcdefgh", "/v1/chat/completions", "success",
		`{"prompt_tokens":10,"completion_tokens":5}`, 10, 5, 0.001)

	wantKey := stamp.In(time.Local).Format("2006-01-02")
	if _, err := repo.RollupUsageHistoryBeforeDate(ctx, pgCutoff(30)); err != nil {
		t.Fatalf("RollupUsageHistoryBeforeDate: %v", err)
	}
	d := pgDaily(t, repo, wantKey)
	if got := pgDailyInt(t, d, "requests"); got != 1 {
		t.Errorf("%s requests = %d, want 1", wantKey, got)
	}
}
