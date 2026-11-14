package obs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/olegpustovit/agentpack/internal/paths"
)

const MaxSpoolBytes = 16 << 20

type SpoolOptions struct {
	Proc string
	Now  func() time.Time
}

type Spool struct {
	queue      *Queue
	directory  string
	options    SpoolOptions
	closed     atomic.Bool
	once       sync.Once
	stop, done chan struct{}
	firstError error
}

// OpenSpool stays filesystem-free until the first event, including on Close.
func OpenSpool(home string, options SpoolOptions) (*Spool, error) {
	if options.Proc == "" || len(options.Proc) > 32 || strings.IndexFunc(options.Proc, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) >= 0 {
		return nil, errors.New("invalid diagnostic process name")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	spool := &Spool{queue: NewQueue(), directory: filepath.Join(home, "logs", "spool"), options: options, stop: make(chan struct{}), done: make(chan struct{})}
	go spool.run()
	return spool, nil
}

func (s *Spool) Write(event Event) {
	if s.closed.Load() {
		s.queue.Dropped.Add(1)
		return
	}
	s.queue.Write(event.Sanitized())
}
func (s *Spool) Dropped() uint64 { return s.queue.Dropped.Load() }
func (s *Spool) Close() error {
	s.once.Do(func() { s.closed.Store(true); close(s.stop) })
	<-s.done
	return s.firstError
}

func (s *Spool) run() {
	defer close(s.done)
	record := func(err error) {
		if err != nil && s.firstError == nil {
			s.firstError = err
			log.Printf("diagnostic spool is not saved: %s", redactString(err.Error()))
		}
	}
	ready := false
	write := func(event Event) {
		if !ready {
			if err := paths.EnsurePrivateDirectory(s.directory); err != nil {
				record(err)
				return
			}
			ready = true
		}
		path := filepath.Join(s.directory, fmt.Sprintf("%s-%d-%d.ndjson", s.options.Proc, os.Getpid(), s.options.Now().Unix()/60))
		if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
			record(errors.New("spool must be a regular file"))
			return
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			record(err)
			return
		}
		data, err := json.Marshal(event)
		if err != nil {
			record(err)
			return
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			record(err)
			return
		}
		if err := file.Chmod(0600); err != nil {
			record(errors.Join(err, file.Close()))
			return
		}
		// Close each line so an idle Windows process cannot prevent ingestion/deletion.
		_, err = file.Write(append(data, '\n'))
		record(errors.Join(err, file.Close()))
	}
	for {
		select {
		case event := <-s.queue.Events:
			write(event)
		case <-s.stop:
			for range len(s.queue.Events) {
				write(<-s.queue.Events)
			}
			return
		}
	}
}

type SpoolStats struct {
	Files, Events, Malformed, DroppedFiles int
	DroppedBytes                           int64
}

type spoolFile struct {
	name     string
	size     int64
	modified time.Time
}

func spoolFiles(directory string) ([]spoolFile, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []spoolFile
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".ndjson") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		files = append(files, spoolFile{entry.Name(), info.Size(), info.ModTime()})
	}
	slices.SortFunc(files, func(a, b spoolFile) int {
		if cmp := a.modified.Compare(b.modified); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.name, b.name)
	})
	return files, nil
}

func (s *Store) IngestSpool(ctx context.Context) (SpoolStats, error) {
	var stats SpoolStats
	directory := filepath.Join(s.directory, "spool")
	files, err := spoolFiles(directory)
	if err != nil {
		return stats, err
	}
	var total int64
	for _, file := range files {
		total += file.size
	}
	for len(files) > 0 && total > MaxSpoolBytes {
		file := files[0]
		if err := os.Remove(filepath.Join(directory, file.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return stats, err
		}
		total -= file.size
		stats.DroppedFiles++
		stats.DroppedBytes += file.size
		files = files[1:]
	}
	cutoff := s.options.Now().Unix()/60 - 2
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		stem := strings.TrimSuffix(file.name, ".ndjson")
		at := strings.LastIndexByte(stem, '-')
		if at < 0 {
			continue
		}
		minute, err := strconv.ParseInt(stem[at+1:], 10, 64)
		if err != nil || minute > cutoff {
			continue
		}
		path := filepath.Join(directory, file.name)
		parsed, err := s.ingestSpoolFile(ctx, path)
		stats.Events += parsed.Events
		stats.Malformed += parsed.Malformed
		if err != nil {
			return stats, err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return stats, err
		}
		stats.Files++
	}
	return stats, nil
}

func (s *Store) ingestSpoolFile(ctx context.Context, path string) (SpoolStats, error) {
	var stats SpoolStats
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return stats, nil
	}
	if err != nil {
		return stats, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	batch := make([]Event, 0, 256)
	for {
		line := make([]byte, 0, 1024)
		oversized := false
		var readErr error
		for {
			var part []byte
			part, readErr = reader.ReadSlice('\n')
			if len(line)+len(part) > MaxEventBytes+1 {
				oversized = true
			}
			if !oversized {
				line = append(line, part...)
			}
			if !errors.Is(readErr, bufio.ErrBufferFull) {
				break
			}
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return stats, readErr
		}
		if len(line) == 0 && !oversized && errors.Is(readErr, io.EOF) {
			break
		}
		var event Event
		if oversized || errors.Is(readErr, io.EOF) || json.Unmarshal(line, &event) != nil || event.TS.IsZero() || !validTrace(Trace{event.TraceID, event.SpanID}) {
			stats.Malformed++
		} else {
			batch = append(batch, event)
		}
		if len(batch) == 256 {
			if err := s.Import(ctx, batch); err != nil {
				return stats, err
			}
			stats.Events += len(batch)
			batch = batch[:0]
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if err := s.Import(ctx, batch); err != nil {
		return stats, err
	}
	stats.Events += len(batch)
	return stats, nil
}
