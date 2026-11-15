package obs

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

// ForwardErrors sends bounded batches from retained days, acknowledging only committed uploads.
func (s *Store) ForwardErrors(ctx context.Context, main string, send func(context.Context, []Event) error) error {
	if main == "" {
		return nil
	}
	if err := s.Flush(ctx); err != nil {
		return err
	}
	days, err := logDays(filepath.Dir(s.directory))
	if err != nil {
		return err
	}
	for _, day := range days {
		db, err := openLogFile(filepath.Join(s.directory, day), true)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		err = forwardDay(ctx, db, main, send)
		if err := errors.Join(err, db.Close()); err != nil {
			return err
		}
	}
	return nil
}

func forwardDay(ctx context.Context, db *sql.DB, main string, send func(context.Context, []Event) error) error {
	key := "forward:" + main
	var cursor int64
	err := db.QueryRowContext(ctx, "SELECT CAST(value AS INTEGER) FROM meta WHERE key=?", key).Scan(&cursor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for {
		events, _, err := queryDay(ctx, db, Filter{Limit: 16, errorAfter: &cursor})
		if err != nil || len(events) == 0 {
			return err
		}
		if err := send(ctx, events); err != nil {
			return err
		}
		cursor = events[len(events)-1].errorPosition
		if _, err := db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES (?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE CAST(value AS INTEGER)<CAST(excluded.value AS INTEGER)`, key, strconv.FormatInt(cursor, 10)); err != nil {
			return err
		}
	}
}
