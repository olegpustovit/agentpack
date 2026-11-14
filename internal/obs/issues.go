package obs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/olegpustovit/agentpack/internal/state"
)

const issueFlushInterval = 30 * time.Second

type issueVersion struct {
	Count, FirstSeen, LastSeen int64
	Trace                      string
}
type dayIssue struct {
	Fingerprint, Node, Kind, Title string
	FirstSeen                      int64
	Hours                          map[int64]int64
	Versions                       map[string]issueVersion
}
type pendingIssue struct {
	Day     string
	Counter dayIssue
	Sample  string
}

type issueBatch struct {
	store    *Store
	tx       *sql.Tx
	counters map[string]*dayIssue
	samples  map[string]string
	sources  map[string]int64
}

func issueKey(node, fingerprint string) string {
	hash := sha256.Sum256([]byte(node + "\x00" + fingerprint))
	return "issue:" + hex.EncodeToString(hash[:])
}

func (b *issueBatch) accept(event Event) (bool, error) {
	if event.source != "" {
		key := "spool:" + event.source
		cursor, ok := b.sources[key]
		if !ok {
			var value string
			err := b.tx.QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&value)
			if err == nil {
				if _, err := fmt.Sscan(value, &cursor); err != nil {
					return false, err
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return false, err
			}
			b.sources[key] = cursor
		}
		if event.sourceLine <= cursor {
			return false, nil
		}
		b.sources[key] = event.sourceLine
	}
	if b.store.options.State == nil || event.Level != "error" || event.NodeID != b.store.options.Metadata.NodeID {
		return true, nil
	}
	// Live producers finalize once; forwarded sampled events also keep their identity.
	var exists int
	trace, _ := hex.DecodeString(event.TraceID)
	span, _ := hex.DecodeString(event.SpanID)
	if err := b.tx.QueryRow("SELECT count(*) FROM events WHERE trace_id=? AND span_id=? AND node_id=?", trace, span, event.NodeID).Scan(&exists); err != nil {
		return false, err
	}
	if exists != 0 {
		return false, nil
	}
	key := issueKey(event.NodeID, event.Fingerprint)
	counter := b.counters[key]
	if counter == nil {
		counter = &dayIssue{Fingerprint: event.Fingerprint, Node: event.NodeID, Kind: event.Kind, Title: issueTitle(event), FirstSeen: event.TS.UnixMilli(), Hours: map[int64]int64{}, Versions: map[string]issueVersion{}}
		var data string
		err := b.tx.QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&data)
		if err == nil {
			if err := json.Unmarshal([]byte(data), counter); err != nil {
				return false, err
			}
			if counter.Fingerprint != event.Fingerprint || counter.Node != event.NodeID || counter.Hours == nil || counter.Versions == nil {
				return false, errors.New("invalid diagnostic issue counter")
			}
			if err := b.tx.QueryRow("SELECT value FROM meta WHERE key=?", "sample:"+key).Scan(&data); err != nil {
				return false, err
			}
			b.samples[key] = data
		} else if errors.Is(err, sql.ErrNoRows) {
			encoded, err := json.Marshal(event.limited(16 << 10))
			if err != nil {
				return false, err
			}
			b.samples[key] = string(encoded)
		} else {
			return false, err
		}
		b.counters[key] = counter
	}
	at := event.TS.UnixMilli()
	counter.FirstSeen = min(counter.FirstSeen, at)
	hour := event.TS.Unix() / 3600
	counter.Hours[hour]++
	version, _ := event.Attrs["version"].(string)
	version = boundedString(version, 256)
	occurrence := counter.Versions[version]
	if occurrence.Count == 0 {
		occurrence.FirstSeen = at
	}
	occurrence.Count++
	occurrence.FirstSeen = min(occurrence.FirstSeen, at)
	if at >= occurrence.LastSeen {
		occurrence.LastSeen, occurrence.Trace = at, event.TraceID
	}
	counter.Versions[version] = occurrence
	n := counter.Hours[hour]
	return n <= 20 || (n-20)%50 == 0, nil
}

func (b *issueBatch) save() error {
	for key, cursor := range b.sources {
		if _, err := b.tx.Exec("INSERT INTO meta(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, fmt.Sprint(cursor)); err != nil {
			return err
		}
	}
	for key, counter := range b.counters {
		data, err := json.Marshal(counter)
		if err != nil {
			return err
		}
		if _, err := b.tx.Exec("INSERT INTO meta(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, string(data)); err != nil {
			return err
		}
		if _, err := b.tx.Exec("INSERT OR IGNORE INTO meta(key,value) VALUES (?,?)", "sample:"+key, b.samples[key]); err != nil {
			return err
		}
	}
	return nil
}

func (b *issueBatch) committed() {
	for key, counter := range b.counters {
		b.store.pendingIssues[b.store.day+":"+key] = pendingIssue{b.store.day, *counter, b.samples[key]}
	}
}

func issueTitle(event Event) string {
	if chain, ok := event.Attrs["errors"].([]any); ok && len(chain) > 0 {
		if first, ok := chain[0].(map[string]any); ok {
			if message, ok := first["message"].(string); ok {
				return boundedString(redactString(message), 256)
			}
		}
	}
	if message, ok := event.Attrs["message"].(string); ok {
		return boundedString(redactString(message), 256)
	}
	return boundedString(event.Name+" failed", 256)
}

func (s *Store) publishIssues(force bool) error {
	if s.options.State == nil {
		return nil
	}
	groups := map[string][]string{}
	for key, pending := range s.pendingIssues {
		fingerprint := issueKey(pending.Counter.Node, pending.Counter.Fingerprint)
		if force || s.options.Now().Sub(s.published[fingerprint]) >= issueFlushInterval {
			groups[fingerprint] = append(groups[fingerprint], key)
		}
	}
	for fingerprint, keys := range groups {
		slices.Sort(keys)
		err := state.Transaction(context.Background(), s.options.State.DB, func(tx *sql.Tx) error {
			for _, key := range keys {
				if err := publishIssue(tx, s.pendingIssues[key]); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, key := range keys {
			delete(s.pendingIssues, key)
		}
		s.published[fingerprint] = s.options.Now()
	}
	return nil
}

func publishIssue(tx *sql.Tx, pending pendingIssue) error {
	counter := pending.Counter
	if _, err := tx.Exec("INSERT INTO issues(fingerprint,kind,title,first_seen,sample) VALUES (?,?,?,?,?) ON CONFLICT(fingerprint) DO UPDATE SET first_seen=excluded.first_seen WHERE excluded.first_seen<issues.first_seen", counter.Fingerprint, counter.Kind, counter.Title, counter.FirstSeen, pending.Sample); err != nil {
		return err
	}
	var status, fix string
	if err := tx.QueryRow("SELECT status,resolved_version FROM issues WHERE fingerprint=?", counter.Fingerprint).Scan(&status, &fix); err != nil {
		return err
	}
	versions := make([]string, 0, len(counter.Versions))
	for version := range counter.Versions {
		versions = append(versions, version)
	}
	slices.Sort(versions)
	reopen := false
	for _, version := range versions {
		occurrence := counter.Versions[version]
		var published int64
		err := tx.QueryRow("SELECT count FROM issue_log_cursors WHERE day=? AND fingerprint=? AND node_id=? AND version=?", pending.Day, counter.Fingerprint, counter.Node, version).Scan(&published)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		delta := occurrence.Count - published
		if delta < 0 {
			return errors.New("diagnostic count moved backwards")
		}
		if delta == 0 {
			continue
		}
		trace, _ := hex.DecodeString(occurrence.Trace)
		if _, err := tx.Exec(`INSERT INTO issue_counts(fingerprint,node_id,count,first_seen,last_seen,last_version,last_trace) VALUES (?,?,?,?,?,?,?)
ON CONFLICT(fingerprint,node_id) DO UPDATE SET count=issue_counts.count+excluded.count,first_seen=min(issue_counts.first_seen,excluded.first_seen),last_seen=max(issue_counts.last_seen,excluded.last_seen),last_version=CASE WHEN excluded.last_seen>=issue_counts.last_seen THEN excluded.last_version ELSE issue_counts.last_version END,last_trace=CASE WHEN excluded.last_seen>=issue_counts.last_seen THEN excluded.last_trace ELSE issue_counts.last_trace END`, counter.Fingerprint, counter.Node, delta, occurrence.FirstSeen, occurrence.LastSeen, version, trace); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO issue_log_cursors(day,fingerprint,node_id,version,count) VALUES (?,?,?,?,?) ON CONFLICT(day,fingerprint,node_id,version) DO UPDATE SET count=excluded.count", pending.Day, counter.Fingerprint, counter.Node, version, occurrence.Count); err != nil {
			return err
		}
		if status == "resolved" && versionAtLeast(version, fix) {
			reopen = true
		}
	}
	if reopen {
		_, err := tx.Exec("UPDATE issues SET status='open',regressed=1 WHERE fingerprint=?", counter.Fingerprint)
		return err
	}
	return nil
}

func versionAtLeast(incoming, fixed string) bool {
	a, aErr := semver.NewVersion(incoming)
	b, bErr := semver.NewVersion(fixed)
	return aErr != nil || bErr != nil || !a.LessThan(b)
}

func (s *Store) recoverIssues() error {
	if s.options.State == nil {
		return nil
	}
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		day := strings.TrimSuffix(entry.Name(), ".db")
		if _, err := time.Parse(time.DateOnly, day); err != nil {
			continue
		}
		if err := s.openDay(day); err != nil {
			return err
		}
		rows, err := s.db.Query("SELECT key,value FROM meta WHERE key LIKE 'issue:%'")
		if err != nil {
			return err
		}
		counters := map[string]dayIssue{}
		for rows.Next() {
			var key, data string
			if err := rows.Scan(&key, &data); err != nil {
				rows.Close()
				return err
			}
			var counter dayIssue
			if err := json.Unmarshal([]byte(data), &counter); err != nil {
				rows.Close()
				return err
			}
			if key != issueKey(counter.Node, counter.Fingerprint) || counter.Hours == nil || counter.Versions == nil {
				rows.Close()
				return errors.New("invalid diagnostic issue counter")
			}
			counters[key] = counter
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		for key, counter := range counters {
			var sample string
			if err := s.db.QueryRow("SELECT value FROM meta WHERE key=?", "sample:"+key).Scan(&sample); err != nil {
				return err
			}
			s.pendingIssues[day+":"+key] = pendingIssue{day, counter, sample}
		}
	}
	return s.publishIssues(true)
}

func (s *Store) pruneIssueCursors(cutoff string) error {
	if s.options.State == nil {
		return nil
	}
	_, err := s.options.State.DB.Exec("DELETE FROM issue_log_cursors WHERE day < ?", cutoff)
	return err
}
