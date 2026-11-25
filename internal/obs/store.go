package obs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/olegpustovit/agentpack/internal/state"
)

const daySchema = `
CREATE TABLE IF NOT EXISTS events (
 id INTEGER PRIMARY KEY, ts INTEGER NOT NULL, duration_us INTEGER NOT NULL,
 trace_id BLOB NOT NULL, span_id BLOB NOT NULL, node_id TEXT NOT NULL, proc TEXT NOT NULL,
 kind TEXT NOT NULL, name TEXT NOT NULL, level TEXT NOT NULL, status INTEGER NOT NULL,
 error_trace INTEGER NOT NULL DEFAULT 0,
 session_id TEXT, user TEXT NOT NULL, fingerprint TEXT, stack BLOB, attrs BLOB NOT NULL,
 UNIQUE(trace_id, span_id, node_id)
);
CREATE TABLE IF NOT EXISTS stacks (hash BLOB PRIMARY KEY, text TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS events_ts ON events(ts);
CREATE INDEX IF NOT EXISTS events_trace ON events(trace_id);
CREATE INDEX IF NOT EXISTS events_fingerprint ON events(fingerprint) WHERE fingerprint IS NOT NULL;
CREATE INDEX IF NOT EXISTS events_session ON events(session_id) WHERE session_id IS NOT NULL;
`

type StoreOptions struct {
	Metadata Metadata
	State    *state.Store
	// StateContext ends shared-state maintenance while the day log can still drain.
	StateContext  context.Context
	Now           func() time.Time
	FlushInterval time.Duration
	SweepInterval time.Duration
	MaxDayBytes   int64
}

type Store struct {
	queue      *Queue
	directory  string
	options    StoreOptions
	flush      chan chan error
	stop, done chan struct{}
	closed     atomic.Bool
	closeOnce  sync.Once
	// Only the writer goroutine accesses the following fields.
	db            *sql.DB
	day           string
	swept         time.Time
	firstError    error
	pendingIssues map[string]pendingIssue
	published     map[string]time.Time
}

func Open(home string, options StoreOptions) (*Store, error) {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.FlushInterval <= 0 {
		options.FlushInterval = 250 * time.Millisecond
	}
	if options.SweepInterval <= 0 {
		options.SweepInterval = time.Hour
	}
	if options.MaxDayBytes <= 0 {
		options.MaxDayBytes = 256 << 20
	}
	directory := filepath.Join(home, "logs")
	if err := paths.EnsurePrivateDirectory(directory); err != nil {
		return nil, err
	}
	store := &Store{queue: NewQueue(), directory: directory, options: options, flush: make(chan chan error), stop: make(chan struct{}), done: make(chan struct{}), pendingIssues: map[string]pendingIssue{}, published: map[string]time.Time{}}
	err := store.recoverIssues()
	if err == nil {
		err = store.sweep(context.Background(), false)
	}
	if err != nil {
		if store.db != nil {
			err = errors.Join(err, store.db.Close())
		}
		return nil, err
	}
	go store.run()
	return store, nil
}

func (s *Store) Write(event Event) {
	if s.closed.Load() {
		s.queue.Dropped.Add(1)
		return
	}
	s.queue.Write(event.Sanitized())
}

// Import applies backpressure to bulk ingestion; live event producers use Write.
func (s *Store) Import(ctx context.Context, events []Event) error {
	for _, event := range events {
		if s.closed.Load() {
			return errors.New("diagnostic store closed")
		}
		select {
		case s.queue.Events <- event.Sanitized():
		case <-s.done:
			return errors.New("diagnostic store closed")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Flush(ctx)
}

func (s *Store) Dropped() uint64 { return s.queue.Dropped.Load() }

func (s *Store) Flush(ctx context.Context) error {
	reply := make(chan error, 1)
	select {
	case s.flush <- reply:
	case <-s.done:
		return s.firstError
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() { s.closed.Store(true); close(s.stop) })
	<-s.done
	return s.firstError
}

func (s *Store) recordError(err error) {
	if err != nil && s.firstError == nil {
		s.firstError = err
		log.Printf("diagnostic events are not saved: %s", redactString(err.Error()))
	}
}

// Give each flush one budget, including all batches and issue transactions.
const flushBudget = 5 * time.Second

func (s *Store) run() {
	defer close(s.done)
	sweep := time.NewTicker(s.options.SweepInterval)
	defer sweep.Stop()
	var timer *time.Timer
	var flushDue <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	armFlush := func() {
		if flushDue != nil {
			return
		}
		if timer == nil {
			timer = time.NewTimer(s.options.FlushInterval)
		} else {
			timer.Reset(s.options.FlushInterval)
		}
		flushDue = timer.C
	}
	batch := make([]Event, 0, 256)
	var pendingError error
	failed := func(err error) {
		s.recordError(err)
		if pendingError == nil {
			pendingError = err
		}
	}
	flushEvents := func(ctx context.Context) {
		if flushDue != nil {
			timer.Stop()
			flushDue = nil
		}
		if len(batch) != 0 {
			if err := s.writeBatch(ctx, batch); err != nil {
				failed(fmt.Errorf("write diagnostic day log: %w", err))
			}
			batch = batch[:0]
		}
	}
	flush := func(ctx context.Context, force bool) {
		flushEvents(ctx)
		failed(s.maintainState(ctx, force))
		if s.options.Now().Sub(s.swept) >= s.options.SweepInterval {
			failed(s.sweep(ctx, true))
		}
		// Coalesced issue counts still need to publish if no more events arrive.
		if len(s.pendingIssues) != 0 {
			armFlush()
		}
	}
	withBudget := func(run func(context.Context)) {
		ctx, cancel := context.WithTimeout(context.Background(), flushBudget)
		defer cancel()
		run(ctx)
	}
	add := func(event Event) bool {
		batch = append(batch, event)
		if len(batch) == 1 {
			armFlush()
		}
		return len(batch) == cap(batch)
	}
	drain := func(ctx context.Context, force bool) {
		for range len(s.queue.Events) {
			if add(<-s.queue.Events) {
				flushEvents(ctx)
			}
		}
		flush(ctx, force)
	}
	flushPeriodically := func(ctx context.Context) { flush(ctx, false) }
	if len(s.pendingIssues) != 0 {
		armFlush()
	}
	for !s.closed.Load() {
		select {
		case event := <-s.queue.Events:
			if add(event) {
				withBudget(flushPeriodically)
			}
		case <-flushDue:
			withBudget(flushPeriodically)
		case <-sweep.C:
			withBudget(flushPeriodically)
		case reply := <-s.flush:
			withBudget(func(ctx context.Context) { drain(ctx, false) })
			reply <- pendingError
			pendingError = nil
		case <-s.stop:
		}
	}
	withBudget(func(ctx context.Context) { drain(ctx, true) })
	if s.db != nil {
		s.recordError(s.db.Close())
	}
}

func (s *Store) openDay(ctx context.Context, day string) error {
	if s.db != nil && s.day == day {
		return nil
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			return err
		}
		s.db = nil
	}
	path := filepath.Join(s.directory, day+".db")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("log database must be a regular file: %s", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err := errors.Join(file.Chmod(0600), file.Close()); err != nil {
		return err
	}
	db, err := state.OpenPlain(path, state.PlainOptions{})
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, daySchema); err != nil {
		return errors.Join(err, db.Close())
	}
	var hasFlag bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pragma_table_info('events') WHERE name='error_trace')").Scan(&hasFlag); err != nil {
		return errors.Join(err, db.Close())
	}
	if !hasFlag {
		if _, err := db.ExecContext(ctx, `BEGIN IMMEDIATE; ALTER TABLE events ADD COLUMN error_trace INTEGER NOT NULL DEFAULT 0;
UPDATE events SET error_trace=id WHERE trace_id IN (SELECT trace_id FROM events WHERE level='error'); COMMIT;`); err != nil {
			return errors.Join(err, db.Close())
		}
	}
	if _, err := db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS events_error_trace ON events(error_trace) WHERE error_trace>0"); err != nil {
		return errors.Join(err, db.Close())
	}
	s.db, s.day = db, day
	return nil
}

func (s *Store) writeBatch(ctx context.Context, events []Event) error {
	byDay := map[string][]Event{}
	for _, event := range events {
		day := event.TS.UTC().Format(time.DateOnly)
		if day < s.options.Now().UTC().AddDate(0, 0, -5).Format(time.DateOnly) {
			s.queue.Dropped.Add(1)
			continue
		}
		byDay[day] = append(byDay[day], event)
	}
	days := make([]string, 0, len(byDay))
	for day := range byDay {
		days = append(days, day)
	}
	slices.Sort(days)
	for _, day := range days {
		if err := s.openDay(ctx, day); err != nil {
			return err
		}
		if err := s.insert(ctx, byDay[day]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) insert(ctx context.Context, events []Event) error {
	if s.options.State != nil {
		aliases := map[string]bool{}
		rows, err := s.options.State.Reader.QueryContext(ctx, "SELECT node_id FROM local_node_aliases")
		if err != nil {
			return err
		}
		for rows.Next() {
			var alias string
			if err := rows.Scan(&alias); err != nil {
				rows.Close()
				return err
			}
			aliases[alias] = true
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		node, err := s.options.State.NodeID(ctx)
		if err != nil {
			return err
		}
		if aliases[s.options.Metadata.NodeID] {
			s.options.Metadata.NodeID = node
		}
		for i := range events {
			if aliases[events[i].NodeID] {
				events[i].NodeID = node
			}
		}
	}
	used, err := logFileSize(filepath.Join(s.directory, s.day+".db"))
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO events(ts,duration_us,trace_id,span_id,node_id,proc,kind,name,level,status,session_id,user,fingerprint,stack,attrs) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,jsonb(?))`)
	if err != nil {
		return err
	}
	defer statement.Close()
	stackStatement, err := tx.PrepareContext(ctx, "INSERT OR IGNORE INTO stacks(hash,text) VALUES (?,?)")
	if err != nil {
		return err
	}
	defer stackStatement.Close()
	insert := func(event Event, attrs []byte) error {
		var stack []byte
		if event.Stack != "" {
			hash := sha256.Sum256([]byte(event.Stack))
			stack = hash[:]
			if _, err := stackStatement.ExecContext(ctx, stack, event.Stack); err != nil {
				return err
			}
		}
		trace, _ := hex.DecodeString(event.TraceID)
		span, _ := hex.DecodeString(event.SpanID)
		var session, fingerprint any
		if event.SessionID != "" {
			session = event.SessionID
		}
		if event.Fingerprint != "" {
			fingerprint = event.Fingerprint
		}
		_, err := statement.ExecContext(ctx, event.TS.UnixMicro(), event.DurationUS, trace, span, event.NodeID, event.Proc, event.Kind, event.Name, event.Level, event.Status, session, event.User, fingerprint, stack, string(attrs))
		return err
	}
	accounting := issueBatch{store: s, tx: tx, counters: map[string]*dayIssue{}, samples: map[string]string{}, sources: map[string]int64{}}
	dropped := 0
	for _, event := range events {
		keep, err := accounting.accept(event)
		if err != nil {
			return err
		}
		if !keep {
			continue
		}
		attrs, err := json.Marshal(event.Attrs)
		if err != nil {
			return err
		}
		// Reserve index/WAL pages conservatively; errors and drop reports survive the cap.
		estimate := int64(16<<10 + 2*len(attrs) + len(event.Stack))
		if event.Level != "error" && !event.ErrorTrace && used+estimate > s.options.MaxDayBytes {
			dropped++
			continue
		}
		used += estimate
		if err := insert(event, attrs); err != nil {
			return err
		}
	}
	if dropped != 0 {
		_, report := Start(WithSink(context.Background(), nil, s.options.Metadata), "obs.drop", "day size cap")
		report.TS = events[0].TS
		report.Set("dropped", dropped)
		report.Set("reason", "day_size_cap")
		event := report.Sanitized()
		attrs := event.Attrs
		encoded, err := json.Marshal(attrs)
		if err != nil {
			return err
		}
		if err := insert(event, encoded); err != nil {
			return err
		}
	}
	if err := accounting.save(); err != nil {
		return err
	}
	// A positive flag is a forwarding position: late failures promote earlier spans beyond saved cursors.
	var position int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(max(error_trace),0) FROM events WHERE error_trace>0").Scan(&position); err != nil {
		return err
	}
	for _, event := range events {
		trace, _ := hex.DecodeString(event.TraceID)
		if _, err := tx.ExecContext(ctx, `UPDATE events SET error_trace=?+id WHERE trace_id=? AND error_trace=0
AND (? OR EXISTS(SELECT 1 FROM events WHERE trace_id=? AND error_trace>0))`, position, trace, event.Level == "error" || event.ErrorTrace, trace); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	accounting.committed()
	s.queue.Dropped.Add(uint64(dropped))
	return nil
}

func logFileSize(path string) (int64, error) {
	var total int64
	for _, name := range []string{path, path + "-wal"} {
		info, err := os.Stat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}

func (s *Store) sweep(ctx context.Context, prune bool) error {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return err
	}
	cutoff := s.options.Now().UTC().AddDate(0, 0, -5).Format(time.DateOnly)
	held := map[string]bool{}
	for _, pending := range s.pendingIssues {
		if pending.Day < cutoff {
			held[pending.Day] = true
		}
	}
	deferred := false
	for _, entry := range entries {
		name := entry.Name()
		if len(name) < len("2006-01-02.db") {
			continue
		}
		day, suffix := name[:10], name[10:]
		if suffix != ".db" && suffix != ".db-wal" && suffix != ".db-shm" {
			continue
		}
		if _, err := time.Parse(time.DateOnly, day); err != nil || day >= cutoff {
			continue
		}
		if held[day] {
			deferred = true
			continue
		}
		if s.db != nil && s.day == day {
			if err := s.db.Close(); err != nil {
				return err
			}
			s.db = nil
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(filepath.Join(s.directory, day+".db"+suffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	if deferred {
		return nil
	}
	if prune {
		if err := s.withStateContext(ctx, func(ctx context.Context) error { return s.pruneIssueCursors(ctx, cutoff) }); err != nil {
			return err
		}
	}
	s.swept = s.options.Now()
	return nil
}
