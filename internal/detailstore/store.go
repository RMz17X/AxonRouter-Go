// Package detailstore is the file-backed home of the request-details
// records: one diagnostic record per request attempt, carrying the
// request/response payload the Request Details page inspects.
//
// These records used to live in PostgreSQL beside the billing ledger.
// They are not billing truth — they are diagnostics, retained for a
// few thousand rows — yet every attempt wrote a ~10 KB jsonb payload
// into the same database that serves connections and settings, and
// every dashboard read grouped that table on the spot. The owner's
// call (2026-11-11): files, fire-and-forget. Appends ride a bounded
// queue drained by one writer goroutine onto append-only JSONL files
// under the data dir; a full queue drops the record and counts it
// rather than ever parking a response goroutine. Reads run over an
// in-memory index of light per-record projections; the payload itself
// is fetched from its file offset only when the inspector asks for
// one record in full.
//
// Retention mirrors the old ring: files hold recordsPerFile records
// each and the newest keepFiles survive, so the store carries the
// latest ~5–6k attempts. On first boot with no files yet, Init seeds
// the store from the legacy PostgreSQL table so the upgrade does not
// blank the page.
package detailstore

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/log"
)

const (
	recordsPerFile = 1000
	keepFiles      = 6
	queueDepth     = 512
	dirName        = "request-details"
)

// errNotReady is returned by reads when Init has not run (or failed):
// an unavailable diagnostics store must read as an error, never as an
// empty page.
var errNotReady = errors.New("detailstore: not initialised")

// envelope is one JSONL line: the row columns plus the payload string,
// byte-identical to what the PostgreSQL `data` column held.
type envelope struct {
	ID           string `json:"id"`
	Timestamp    string `json:"timestamp"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	ConnectionID string `json:"connectionId"`
	Status       string `json:"status"`
	Data         string `json:"data"`
}

// light is the in-memory projection of one record: every field the
// read paths extract, parsed once at ingest so reads never re-parse
// payloads. The payload itself stays on disk (see file/offset/length).
type light struct {
	id, timestamp, provider, model, connectionID, status string
	reqKey                                               string
	latencyTTFT, latencyTotal                            int64
	latencyTotalRaw                                      float64
	tokensJSON                                           string
	account                                              string
	cost                                                 float64
	combo, protocol                                      string
	loopWarning                                          string
	proxy                                                string
	isStream                                             bool
	apiKeyName, clientAPIKey, clientIP                   string
	smartDecisionJSON                                    string
	promptTokens, completionTokens, outputTokens         int64
	hasLatency, hasPromptTokens, hasCompletionTokens     bool
	errorCategory, sampleError, sampleStatus             string

	file   int
	offset int64
	length int64
}

type store struct {
	dir string

	mu      sync.RWMutex
	records []*light
	byID    map[string]*light

	queue   chan db.RequestDetailRecord
	jobs    chan func()
	dropped atomic.Int64

	// Writer-owned state (only the writer goroutine and Init touch it
	// before the goroutine starts).
	seq     int
	file    *os.File
	writer  *bufio.Writer
	inFile  int
	fileLen int64
}

// SeedFunc supplies the legacy rows for the one-time migration, in
// ascending timestamp order.
type SeedFunc func() ([]db.RequestDetailRecord, error)

var (
	current  atomic.Pointer[store]
	initOnce sync.Once
	initErr  error
	// synchronous runs Append inline at the call site. The test
	// harnesses set it: their assertions read records straight after
	// the call that wrote them, mirroring the usage writer's own
	// synchronous test mode.
	synchronous atomic.Bool
)

// Init opens (or creates) the store under <dataDir>/request-details,
// hydrates the index from any existing files, seeds from the legacy
// table on first boot, and starts the writer goroutine. It is called
// once at server boot; later calls return the first call's result.
func Init(dataDir string, seed SeedFunc) error {
	initOnce.Do(func() {
		s := &store{
			dir:   filepath.Join(dataDir, dirName),
			byID:  make(map[string]*light),
			queue: make(chan db.RequestDetailRecord, queueDepth),
			jobs:  make(chan func()),
		}
		if err := s.open(seed); err != nil {
			initErr = err
			log.Error("detailstore", "init failed; request details unavailable", "error", err)
			return
		}
		current.Store(s)
		go s.writeLoop()
	})
	return initErr
}

// OpenForTest installs a store rooted at dir (no legacy seed) as the
// current store, replacing any previous one. Test-only: production
// goes through Init.
func OpenForTest(dir string) error {
	s := &store{
		dir:   filepath.Join(dir, dirName),
		byID:  make(map[string]*light),
		queue: make(chan db.RequestDetailRecord, queueDepth),
		jobs:  make(chan func()),
	}
	if err := s.open(nil); err != nil {
		return err
	}
	current.Store(s)
	go s.writeLoop()
	return nil
}

// SetSynchronousForTest toggles inline execution of Append. While it
// is on, nothing feeds the writer queue, so the writer goroutine and
// inline callers never touch the file state concurrently.
func SetSynchronousForTest(v bool) {
	synchronous.Store(v)
}

func (s *store) open(seed SeedFunc) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("detailstore: mkdir %s: %w", s.dir, err)
	}
	files, err := listFiles(s.dir)
	if err != nil {
		return err
	}
	if len(files) == 0 && seed != nil {
		rows, serr := seed()
		if serr != nil {
			// A failed seed must not block the gateway: start empty and
			// say so loudly — the page is diagnostics, the ledger is not.
			log.Error("detailstore", "seed from legacy table failed; starting empty", "error", serr)
		} else if len(rows) > 0 {
			if err := s.writeSeedFiles(rows); err != nil {
				return err
			}
			files, err = listFiles(s.dir)
			if err != nil {
				return err
			}
		}
	}
	for _, seq := range files {
		if err := s.hydrateFile(seq); err != nil {
			return err
		}
	}
	if len(files) > 0 {
		s.seq = files[len(files)-1]
	} else {
		s.seq = 1
	}
	return s.openCurrent()
}

// writeSeedFiles lands the migrated rows through the same rotation the
// live writer uses, so a seeded store is indistinguishable from one
// that collected the rows itself.
func (s *store) writeSeedFiles(rows []db.RequestDetailRecord) error {
	seq := 1
	var w *bufio.Writer
	var f *os.File
	inFile := 0
	flush := func() error {
		if w != nil {
			if err := w.Flush(); err != nil {
				return err
			}
		}
		if f != nil {
			return f.Close()
		}
		return nil
	}
	for _, rec := range rows {
		if f == nil || inFile >= recordsPerFile {
			if err := flush(); err != nil {
				return err
			}
			if f != nil {
				seq++
			}
			var err error
			f, err = os.OpenFile(filePath(s.dir, seq), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return fmt.Errorf("detailstore: seed file: %w", err)
			}
			w = bufio.NewWriter(f)
			inFile = 0
		}
		line, err := json.Marshal(envelopeOf(rec))
		if err != nil {
			continue
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("detailstore: seed write: %w", err)
		}
		inFile++
	}
	return flush()
}

// hydrateFile folds one JSONL file into the index. A line that does
// not parse — the torn tail of a crash mid-append — is skipped and
// counted in one log line per file, never fatal.
func (s *store) hydrateFile(seq int) error {
	path := filePath(s.dir, seq)
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("detailstore: open %s: %w", path, err)
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var offset int64
	bad := 0
	for {
		line, rerr := r.ReadBytes('\n')
		if len(line) > 0 {
			rec, ok := parseLine(line, seq, offset)
			if !ok {
				bad++
			} else if _, dup := s.byID[rec.id]; !dup {
				s.records = append(s.records, rec)
				s.byID[rec.id] = rec
			}
			offset += int64(len(line))
		}
		if rerr != nil {
			break
		}
	}
	if bad > 0 {
		log.Warn("detailstore", "skipped unreadable lines while loading", "file", path, "lines", bad)
	}
	return nil
}

// openCurrent opens the current sequence file for appending and primes
// the writer counters from what hydration found in it.
func (s *store) openCurrent() error {
	path := filePath(s.dir, s.seq)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("detailstore: open current %s: %w", path, err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("detailstore: stat %s: %w", path, err)
	}
	s.file = f
	s.writer = bufio.NewWriter(f)
	s.fileLen = st.Size()
	s.inFile = 0
	for _, rec := range s.records {
		if rec.file == s.seq {
			s.inFile++
		}
	}
	return nil
}

// writeLoop is the single drain of the append queue: marshal, append,
// index. Jobs (prune, rename) ride a second channel executed by this
// same goroutine, which is what serialises file rewrites against
// appends without a lock. A write error loses the record (counted) —
// the store is diagnostics and must never take the gateway down with
// it.
func (s *store) writeLoop() {
	for {
		select {
		case rec := <-s.queue:
			s.append(rec)
		case job := <-s.jobs:
			job()
		}
	}
}

// runJob executes fn on the writer goroutine and waits for it, so a
// caller that needs the files quiesced (prune, provider rename) runs
// between appends rather than against them.
func (s *store) runJob(fn func()) {
	done := make(chan struct{})
	s.jobs <- func() {
		fn()
		close(done)
	}
	<-done
}

func (s *store) append(rec db.RequestDetailRecord) {
	s.mu.Lock()
	_, dup := s.byID[rec.ID]
	s.mu.Unlock()
	if dup {
		// The PostgreSQL writer deduped on the row key (ON CONFLICT DO
		// NOTHING); the same grain applies here — first writer wins.
		return
	}
	line, err := json.Marshal(envelopeOf(rec))
	if err != nil {
		s.dropped.Add(1)
		return
	}
	line = append(line, '\n')
	if s.inFile >= recordsPerFile {
		if err := s.rotate(); err != nil {
			s.dropped.Add(1)
			log.Error("detailstore", "rotate failed, record dropped", "error", err)
			return
		}
	}
	offset := s.fileLen
	if _, err := s.writer.Write(line); err != nil {
		s.dropped.Add(1)
		log.Error("detailstore", "append failed, record dropped", "error", err)
		return
	}
	if err := s.writer.Flush(); err != nil {
		s.dropped.Add(1)
		log.Error("detailstore", "flush failed, record dropped", "error", err)
		return
	}
	s.fileLen += int64(len(line))
	s.inFile++
	lt := project(rec, s.seq, offset, int64(len(line)))
	s.mu.Lock()
	s.records = append(s.records, lt)
	s.byID[lt.id] = lt
	s.mu.Unlock()
}

// rotate closes the current file, opens the next sequence, and prunes
// files older than the retention window along with their index rows.
func (s *store) rotate() error {
	if err := s.writer.Flush(); err != nil {
		return err
	}
	if err := s.file.Close(); err != nil {
		return err
	}
	s.seq++
	if err := s.openCurrent(); err != nil {
		return err
	}
	oldestKept := s.seq - keepFiles + 1
	if oldestKept <= 1 {
		return nil
	}
	for seq := 1; seq < oldestKept; seq++ {
		_ = os.Remove(filePath(s.dir, seq))
	}
	s.mu.Lock()
	kept := s.records[:0]
	for _, rec := range s.records {
		if rec.file >= oldestKept {
			kept = append(kept, rec)
		} else {
			delete(s.byID, rec.id)
		}
	}
	s.records = kept
	s.mu.Unlock()
	return nil
}

// rewrite rebuilds every file from the records keep accepts, in
// arrival order, applying transform to each stored envelope on the
// way; apply runs on each kept record once the new files are in
// place. It is the one mechanism behind pruning and provider renames,
// and it runs on the writer goroutine. The old files stay untouched
// until every replacement file is complete, so a failure mid-rewrite
// leaves the store exactly as it was.
func (s *store) rewrite(keep func(*light) bool, transform func(*envelope), apply func(*light)) (int, error) {
	if err := s.writer.Flush(); err != nil {
		return 0, err
	}
	if err := s.file.Close(); err != nil {
		return 0, err
	}
	resume := func(err error) (int, error) {
		files, lerr := listFiles(s.dir)
		if lerr == nil && len(files) > 0 {
			s.seq = files[len(files)-1]
		} else {
			s.seq = 1
		}
		_ = s.openCurrent()
		return 0, err
	}

	s.mu.RLock()
	recs := make([]*light, len(s.records))
	copy(recs, s.records)
	s.mu.RUnlock()

	handles := make(map[int]*os.File)
	defer func() {
		for _, f := range handles {
			f.Close()
		}
	}()
	readLine := func(lt *light) ([]byte, error) {
		f, ok := handles[lt.file]
		if !ok {
			var err error
			f, err = os.Open(filePath(s.dir, lt.file))
			if err != nil {
				return nil, err
			}
			handles[lt.file] = f
		}
		buf := make([]byte, lt.length)
		if _, err := f.ReadAt(buf, lt.offset); err != nil {
			return nil, err
		}
		return buf, nil
	}

	type placement struct {
		lt           *light
		file         int
		offset, size int64
	}
	var kept []*light
	var placements []placement
	dropped := 0
	seq := 0
	var out *os.File
	var w *bufio.Writer
	var outLen int64
	inFile := 0
	closeOut := func() error {
		if w != nil {
			if err := w.Flush(); err != nil {
				return err
			}
		}
		if out != nil {
			return out.Close()
		}
		return nil
	}
	for _, lt := range recs {
		if !keep(lt) {
			dropped++
			continue
		}
		line, err := readLine(lt)
		if err != nil {
			_ = closeOut()
			return resume(fmt.Errorf("detailstore: rewrite read: %w", err))
		}
		if transform != nil {
			var env envelope
			if err := json.Unmarshal(line, &env); err != nil {
				_ = closeOut()
				return resume(fmt.Errorf("detailstore: rewrite decode: %w", err))
			}
			transform(&env)
			line, err = json.Marshal(env)
			if err != nil {
				_ = closeOut()
				return resume(fmt.Errorf("detailstore: rewrite encode: %w", err))
			}
			line = append(line, '\n')
		}
		if out == nil || inFile >= recordsPerFile {
			if err := closeOut(); err != nil {
				return resume(fmt.Errorf("detailstore: rewrite flush: %w", err))
			}
			seq++
			out, err = os.OpenFile(filePath(s.dir, seq)+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return resume(fmt.Errorf("detailstore: rewrite create: %w", err))
			}
			w = bufio.NewWriter(out)
			outLen = 0
			inFile = 0
		}
		placements = append(placements, placement{lt: lt, file: seq, offset: outLen, size: int64(len(line))})
		if _, err := w.Write(line); err != nil {
			_ = closeOut()
			return resume(fmt.Errorf("detailstore: rewrite write: %w", err))
		}
		outLen += int64(len(line))
		inFile++
		kept = append(kept, lt)
	}
	if err := closeOut(); err != nil {
		return resume(fmt.Errorf("detailstore: rewrite flush: %w", err))
	}
	for _, f := range handles {
		f.Close()
	}
	handles = make(map[int]*os.File)

	old, err := listFiles(s.dir)
	if err != nil {
		return resume(err)
	}
	for _, oldSeq := range old {
		_ = os.Remove(filePath(s.dir, oldSeq))
	}
	for i := 1; i <= seq; i++ {
		if err := os.Rename(filePath(s.dir, i)+".tmp", filePath(s.dir, i)); err != nil {
			return resume(fmt.Errorf("detailstore: rewrite swap: %w", err))
		}
	}
	for _, p := range placements {
		p.lt.file, p.lt.offset, p.lt.length = p.file, p.offset, p.size
		if apply != nil {
			apply(p.lt)
		}
	}
	s.mu.Lock()
	s.records = kept
	s.byID = make(map[string]*light, len(kept))
	for _, lt := range kept {
		s.byID[lt.id] = lt
	}
	s.mu.Unlock()
	if seq == 0 {
		seq = 1
	}
	s.seq = seq
	if err := s.openCurrent(); err != nil {
		return dropped, err
	}
	return dropped, nil
}

// Count reports how many records the store currently retains.
func Count() int {
	s, err := ready()
	if err != nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

// Attempt is one retained record's projection, in the raw
// per-attempt grain the grouped reads collapse.
type Attempt struct {
	ID           string
	Timestamp    string
	Provider     string
	Model        string
	ConnectionID string
	Status       string
	ReqKey       string
}

// Attempts returns every retained record's projection in arrival
// order. It backs diagnostics and the test suites that used to read
// the table directly.
func Attempts() ([]Attempt, error) {
	s, err := ready()
	if err != nil {
		return nil, err
	}
	recs := s.snapshot()
	out := make([]Attempt, 0, len(recs))
	for _, lt := range recs {
		out = append(out, Attempt{
			ID: lt.id, Timestamp: lt.timestamp, Provider: lt.provider,
			Model: lt.model, ConnectionID: lt.connectionID, Status: lt.status,
			ReqKey: lt.reqKey,
		})
	}
	return out, nil
}

// defaultMaxRows is the retained-record ceiling the size leg of a
// prune applies when the caller names none — the table prune's
// retentionMaxRows, carried over so the ring's size does not change
// with the storage move.
const defaultMaxRows = 5000

// Prune drops records older than the retention window, then the
// oldest records beyond the ceiling (maxRows, or defaultMaxRows when
// maxRows is not positive) — the file store's form of the old table
// prune, keeping its by-age/by-size split. Retention must be
// positive, as the SQL prune required.
func Prune(retention time.Duration, maxRows int) (byAge, bySize int, err error) {
	s, rerr := ready()
	if rerr != nil {
		return 0, 0, rerr
	}
	if retention <= 0 {
		return 0, 0, fmt.Errorf("detailstore: prune: retention must be positive, got %v", retention)
	}
	if maxRows <= 0 {
		maxRows = defaultMaxRows
	}
	cutoff := time.Now().Add(-retention).UTC().Format("2006-01-02T15:04:05.000Z")
	s.runJob(func() {
		byAge, err = s.rewrite(func(lt *light) bool { return lt.timestamp >= cutoff }, nil, nil)
		if err != nil {
			return
		}
		if excess := len(s.snapshot()) - maxRows; excess > 0 {
			seen := 0
			bySize, err = s.rewrite(func(lt *light) bool {
				seen++
				return seen > excess
			}, nil, nil)
		}
	})
	return byAge, bySize, err
}

// RenameProvider moves every retained record from oldID to newID —
// the file store's half of a provider-node rename, mirroring the
// column-only update the table received (payloads keep the provider
// strings they were recorded with). Best-effort: an uninitialised
// store is not an error, matching the rename's own tolerance for
// absent history.
func RenameProvider(oldID, newID string) error {
	s, err := ready()
	if err != nil || oldID == "" || oldID == newID {
		return nil
	}
	var rerr error
	s.runJob(func() {
		_, rerr = s.rewrite(
			func(lt *light) bool { return true },
			func(env *envelope) {
				if env.Provider == oldID {
					env.Provider = newID
				}
			},
			func(lt *light) {
				if lt.provider == oldID {
					lt.provider = newID
				}
			})
	})
	return rerr
}

// Append queues one record for the file writer. It never blocks: a
// full queue drops the record and counts it. Safe to call before Init
// (the record is dropped).
func Append(rec db.RequestDetailRecord) {
	s := current.Load()
	if s == nil {
		return
	}
	if synchronous.Load() {
		s.append(rec)
		return
	}
	select {
	case s.queue <- rec:
	default:
		dropped := s.dropped.Add(1)
		if dropped == 1 || dropped%100 == 0 {
			log.Warn("detailstore", "append queue full, dropping request details", "dropped_total", dropped)
		}
	}
}

// Dropped reports how many records the fire-and-forget writer lost
// since process start.
func Dropped() int64 {
	s := current.Load()
	if s == nil {
		return 0
	}
	return s.dropped.Load()
}

func ready() (*store, error) {
	s := current.Load()
	if s == nil {
		return nil, errNotReady
	}
	return s, nil
}

func envelopeOf(rec db.RequestDetailRecord) envelope {
	return envelope{
		ID: rec.ID, Timestamp: rec.Timestamp, Provider: rec.Provider,
		Model: rec.Model, ConnectionID: rec.ConnectionID, Status: rec.Status,
		Data: rec.Data,
	}
}

func filePath(dir string, seq int) string {
	return filepath.Join(dir, fmt.Sprintf("details-%06d.jsonl", seq))
}

func listFiles(dir string) ([]int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("detailstore: read dir %s: %w", dir, err)
	}
	var seqs []int
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "details-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		var seq int
		if _, err := fmt.Sscanf(name, "details-%d.jsonl", &seq); err == nil && seq > 0 {
			seqs = append(seqs, seq)
		}
	}
	sort.Ints(seqs)
	return seqs, nil
}

// parseLine decodes one stored line into its light projection.
func parseLine(line []byte, seq int, offset int64) (*light, bool) {
	var env envelope
	if err := json.Unmarshal(line, &env); err != nil || env.ID == "" {
		return nil, false
	}
	rec := db.RequestDetailRecord{
		ID: env.ID, Timestamp: env.Timestamp, Provider: env.Provider,
		Model: env.Model, ConnectionID: env.ConnectionID, Status: env.Status,
		Data: env.Data,
	}
	return project(rec, seq, offset, int64(len(line))), true
}
