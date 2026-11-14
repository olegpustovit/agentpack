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
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/olegpustovit/agentpack/internal/paths"
)

const daySchema = `
CREATE TABLE IF NOT EXISTS events (
 id INTEGER PRIMARY KEY, ts INTEGER NOT NULL, duration_us INTEGER NOT NULL,
 trace_id BLOB NOT NULL, span_id BLOB NOT NULL, node_id TEXT NOT NULL, proc TEXT NOT NULL,
 kind TEXT NOT NULL, name TEXT NOT NULL, level TEXT NOT NULL, status INTEGER NOT NULL,
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
	Metadata      Metadata
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
	db         *sql.DB
	day        string
	swept      time.Time
	firstError error
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
	store := &Store{queue: NewQueue(), directory: directory, options: options, flush: make(chan chan error), stop: make(chan struct{}), done: make(chan struct{})}
	if err := store.sweep(); err != nil {
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

func (s *Store) run() {
	defer close(s.done)
	ticker := time.NewTicker(s.options.FlushInterval)
	defer ticker.Stop()
	batch := make([]Event, 0, 256)
	var pendingError error
	failed := func(err error) {
		s.recordError(err)
		if pendingError == nil {
			pendingError = err
		}
	}
	flush := func() {
		if len(batch) != 0 {
			failed(s.writeBatch(batch))
			batch = batch[:0]
		}
		if s.options.Now().Sub(s.swept) >= s.options.SweepInterval {
			failed(s.sweep())
		}
	}
	add := func(event Event) {
		batch = append(batch, event)
		if len(batch) == 256 {
			flush()
		}
	}
	drain := func() {
		for range len(s.queue.Events) {
			add(<-s.queue.Events)
		}
		flush()
	}
	for {
		select {
		case event := <-s.queue.Events:
			add(event)
		case <-ticker.C:
			flush()
		case reply := <-s.flush:
			drain()
			reply <- pendingError
			pendingError = nil
		case <-s.stop:
			drain()
			if s.db != nil {
				s.recordError(s.db.Close())
			}
			return
		}
	}
}

func (s *Store) openDay(day string) error {
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
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite3", "file:"+url.PathEscape(filepath.ToSlash(absolute))+"?agentpack_plain=1&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000")
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(daySchema); err != nil {
		return errors.Join(err, db.Close())
	}
	s.db, s.day = db, day
	return nil
}

func (s *Store) writeBatch(events []Event) error {
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
		if err := s.openDay(day); err != nil {
			return err
		}
		if err := s.insert(byDay[day]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) insert(events []Event) error {
	used, err := logFileSize(filepath.Join(s.directory, s.day+".db"))
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statement, err := tx.Prepare(`INSERT OR IGNORE INTO events(ts,duration_us,trace_id,span_id,node_id,proc,kind,name,level,status,session_id,user,fingerprint,stack,attrs) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,jsonb(?))`)
	if err != nil {
		return err
	}
	defer statement.Close()
	stackStatement, err := tx.Prepare("INSERT OR IGNORE INTO stacks(hash,text) VALUES (?,?)")
	if err != nil {
		return err
	}
	defer stackStatement.Close()
	insert := func(event Event, attrs []byte) error {
		var stack []byte
		if event.Stack != "" {
			hash := sha256.Sum256([]byte(event.Stack))
			stack = hash[:]
			if _, err := stackStatement.Exec(stack, event.Stack); err != nil {
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
		_, err := statement.Exec(event.TS.UnixMicro(), event.DurationUS, trace, span, event.NodeID, event.Proc, event.Kind, event.Name, event.Level, event.Status, session, event.User, fingerprint, stack, string(attrs))
		return err
	}
	dropped := 0
	for _, event := range events {
		attrs, err := json.Marshal(event.Attrs)
		if err != nil {
			return err
		}
		// Reserve index/WAL pages conservatively; errors and drop reports survive the cap.
		estimate := int64(16<<10 + 2*len(attrs) + len(event.Stack))
		if event.Level != "error" && used+estimate > s.options.MaxDayBytes {
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
	if err := tx.Commit(); err != nil {
		return err
	}
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

func (s *Store) sweep() error {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return err
	}
	cutoff := s.options.Now().UTC().AddDate(0, 0, -5).Format(time.DateOnly)
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
		if s.db != nil && s.day == day {
			if err := s.db.Close(); err != nil {
				return err
			}
			s.db = nil
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(filepath.Join(s.directory, name+suffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	s.swept = s.options.Now()
	return nil
}
