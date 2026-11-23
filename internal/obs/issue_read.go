package obs

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/state"
)

var ErrIssueNotFound = errors.New("diagnostic issue not found")

type IssueCount struct {
	Node        string    `json:"node_id"`
	Count       int64     `json:"count"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	LastVersion string    `json:"last_version"`
	LastTrace   string    `json:"last_trace"`
}

// IssueResolution names the version that fixed an issue.
type IssueResolution struct {
	Version string `json:"version" jsonschema:"Version containing the fix; required."`
	Note    string `json:"note,omitempty" jsonschema:"Optional explanation of the fix."`
}

type Issue struct {
	Fingerprint     string       `json:"fingerprint"`
	Kind            string       `json:"kind"`
	Title           string       `json:"title"`
	Status          string       `json:"status"`
	Regressed       bool         `json:"regressed"`
	FirstSeen       time.Time    `json:"first_seen"`
	LastSeen        time.Time    `json:"last_seen"`
	Count           int64        `json:"count"`
	ResolvedAt      time.Time    `json:"resolved_at,omitzero"`
	ResolvedBy      string       `json:"resolved_by,omitempty"`
	ResolvedVersion string       `json:"resolved_version,omitempty"`
	Note            string       `json:"note,omitempty"`
	Counts          []IssueCount `json:"counts"`
	Sample          *Event       `json:"sample,omitempty"`
	RecentTraces    []string     `json:"recent_traces,omitempty"`
}

func validFingerprint(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func ListIssues(ctx context.Context, home, status string) ([]Issue, error) {
	if status != "" && status != "open" && status != "resolved" && status != "ignored" {
		return nil, errors.New("status must be open, resolved or ignored")
	}
	return readIssues(ctx, home, status, "")
}

func GetIssue(ctx context.Context, home, fingerprint string) (Issue, error) {
	if !validFingerprint(fingerprint) {
		return Issue{}, errors.New("fingerprint must contain 64 hexadecimal characters")
	}
	issues, err := readIssues(ctx, home, "", strings.ToLower(fingerprint))
	if err != nil {
		return Issue{}, err
	}
	if len(issues) == 0 {
		return Issue{}, ErrIssueNotFound
	}
	issue := issues[0]
	events, err := Query(ctx, home, Filter{Fingerprint: issue.Fingerprint, Level: "error", Limit: 100})
	if err != nil {
		return Issue{}, err
	}
	seen := map[string]bool{}
	for _, event := range events {
		if !seen[event.TraceID] {
			seen[event.TraceID] = true
			issue.RecentTraces = append(issue.RecentTraces, event.TraceID)
		}
		if len(issue.RecentTraces) == 10 {
			break
		}
	}
	for _, count := range issue.Counts {
		if count.LastTrace != "" && !seen[count.LastTrace] {
			seen[count.LastTrace] = true
			issue.RecentTraces = append(issue.RecentTraces, count.LastTrace)
		}
	}
	return issue, nil
}

func readIssues(ctx context.Context, home, status, fingerprint string) ([]Issue, error) {
	issues := map[string]*Issue{}
	counts := map[string]map[string]*IssueCount{}
	published := map[string]int64{}
	canonical := map[string]bool{}
	covered := map[string]bool{}
	get := func(fp, kind, title string, at time.Time) *Issue {
		issue := issues[fp]
		if issue == nil {
			issue = &Issue{Fingerprint: fp, Kind: kind, Title: title, Status: "open", FirstSeen: at}
			issues[fp] = issue
			counts[fp] = map[string]*IssueCount{}
		}
		if issue.FirstSeen.IsZero() || (!at.IsZero() && at.Before(issue.FirstSeen)) {
			issue.FirstSeen = at
		}
		return issue
	}
	add := func(issue *Issue, node string, n int64, first, last time.Time, version, trace string) {
		count := counts[issue.Fingerprint][node]
		if count == nil {
			count = &IssueCount{Node: node, FirstSeen: first}
			counts[issue.Fingerprint][node] = count
		}
		count.Count += n
		if count.FirstSeen.IsZero() || first.Before(count.FirstSeen) {
			count.FirstSeen = first
		}
		if !last.Before(count.LastSeen) {
			count.LastSeen = last
			count.LastVersion = version
			count.LastTrace = trace
		}
	}
	db, err := openLogFile(filepath.Join(home, "state.db"), false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if db != nil {
		defer db.Close()
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		where := ""
		args := []any{}
		if fingerprint != "" {
			where = " WHERE fingerprint=?"
			args = append(args, fingerprint)
		}
		sampleColumn := "''"
		if fingerprint != "" {
			sampleColumn = "sample"
		}
		rows, err := tx.QueryContext(ctx, "SELECT fingerprint,kind,title,first_seen,status,regressed,resolved_at,resolved_by,resolved_version,note,"+sampleColumn+" FROM issues"+where, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var fp, kind, title, state, who, version, note, sample string
			var first, resolved int64
			var regressed bool
			if err := rows.Scan(&fp, &kind, &title, &first, &state, &regressed, &resolved, &who, &version, &note, &sample); err != nil {
				rows.Close()
				return nil, err
			}
			issue := get(fp, kind, title, time.UnixMilli(first).UTC())
			issue.Status = state
			issue.Regressed = regressed
			issue.ResolvedBy = who
			issue.ResolvedVersion = version
			issue.Note = note
			if resolved != 0 {
				issue.ResolvedAt = time.UnixMilli(resolved).UTC()
			}
			if sample != "" {
				var event Event
				if err := json.Unmarshal([]byte(sample), &event); err != nil {
					rows.Close()
					return nil, err
				}
				clean := event.Sanitized()
				issue.Sample = &clean
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		rows, err = tx.QueryContext(ctx, "SELECT fingerprint,node_id,count,first_seen,last_seen,last_version,last_trace FROM issue_counts"+where, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var fp, node, version string
			var n, first, last int64
			var trace []byte
			if err := rows.Scan(&fp, &node, &n, &first, &last, &version, &trace); err != nil {
				rows.Close()
				return nil, err
			}
			if issue := issues[fp]; issue != nil {
				add(issue, node, n, time.UnixMilli(first).UTC(), time.UnixMilli(last).UTC(), version, hex.EncodeToString(trace))
				canonical[issueKey(node, fp)] = true
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		rows, err = tx.QueryContext(ctx, "SELECT day,fingerprint,node_id,version,count FROM issue_log_cursors"+where, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var day, fp, node, version string
			var n int64
			if err := rows.Scan(&day, &fp, &node, &version, &n); err != nil {
				rows.Close()
				return nil, err
			}
			published[day+":"+issueKey(node, fp)+":"+version] = n
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		if err := tx.Rollback(); err != nil {
			return nil, err
		}
	}
	days, err := logDays(home)
	if err != nil {
		return nil, err
	}
	for _, day := range days {
		db, err := openLogFile(filepath.Join(home, "logs", day), false)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		err = func() error {
			defer db.Close()
			var exists int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name='meta' AND type='table'").Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return nil
			}
			rows, err := db.QueryContext(ctx, "SELECT key,value FROM meta WHERE key LIKE 'issue:%'")
			if err != nil {
				return err
			}
			pending := []struct {
				key     string
				counter dayIssue
			}{}
			for rows.Next() {
				var key, data string
				var counter dayIssue
				if err := rows.Scan(&key, &data); err != nil {
					rows.Close()
					return err
				}
				if err := json.Unmarshal([]byte(data), &counter); err != nil {
					rows.Close()
					return err
				}
				if key != issueKey(counter.Node, counter.Fingerprint) || counter.Versions == nil {
					rows.Close()
					return errors.New("invalid diagnostic issue counter")
				}
				if fingerprint == "" || counter.Fingerprint == fingerprint {
					pending = append(pending, struct {
						key     string
						counter dayIssue
					}{key, counter})
				}
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return err
			}
			for _, item := range pending {
				counter := item.counter
				dayKey := strings.TrimSuffix(day, ".db") + ":" + item.key
				covered[dayKey] = true
				issue := get(counter.Fingerprint, counter.Kind, counter.Title, time.UnixMilli(counter.FirstSeen).UTC())
				for version, occurrence := range counter.Versions {
					delta := occurrence.Count - published[dayKey+":"+version]
					// Counts lost to a crash after publication are already included; skip them.
					if delta <= 0 {
						continue
					}
					add(issue, counter.Node, delta, time.UnixMilli(occurrence.FirstSeen).UTC(), time.UnixMilli(occurrence.LastSeen).UTC(), version, occurrence.Trace)
					if issue.Status == "resolved" && occurrence.LastSeen > issue.ResolvedAt.UnixMilli() && versionAtLeast(version, issue.ResolvedVersion) {
						issue.Status = "open"
						issue.Regressed = true
					}
				}
				if fingerprint != "" && issue.Sample == nil {
					var sample string
					if err := db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", "sample:"+item.key).Scan(&sample); err != nil {
						return err
					}
					var event Event
					if err := json.Unmarshal([]byte(sample), &event); err != nil {
						return err
					}
					clean := event.Sanitized()
					issue.Sample = &clean
				}
			}
			return nil
		}()
		if err != nil {
			return nil, err
		}
	}
	events, err := queryEvents(ctx, home, Filter{Level: "error", Fingerprint: fingerprint})
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		key := issueKey(event.NodeID, event.Fingerprint)
		if event.source == "" && (covered[event.TS.UTC().Format(time.DateOnly)+":"+key] || canonical[key]) {
			continue
		}
		issue := get(event.Fingerprint, event.Kind, issueTitle(event), event.TS)
		version, _ := event.Attrs["version"].(string)
		add(issue, event.NodeID, 1, event.TS, event.TS, version, event.TraceID)
		if fingerprint != "" && issue.Sample == nil {
			clean := event.Sanitized()
			issue.Sample = &clean
		}
		if issue.Status == "resolved" && event.TS.After(issue.ResolvedAt) && versionAtLeast(version, issue.ResolvedVersion) {
			issue.Status = "open"
			issue.Regressed = true
		}
	}
	result := []Issue{}
	for fp, issue := range issues {
		issue.Counts = []IssueCount{}
		for _, count := range counts[fp] {
			issue.Count += count.Count
			if count.LastSeen.After(issue.LastSeen) {
				issue.LastSeen = count.LastSeen
			}
			issue.Counts = append(issue.Counts, *count)
		}
		slices.SortFunc(issue.Counts, func(a, b IssueCount) int { return strings.Compare(a.Node, b.Node) })
		if status == "" || issue.Status == status {
			result = append(result, *issue)
		}
	}
	slices.SortFunc(result, func(a, b Issue) int {
		if order := b.LastSeen.Compare(a.LastSeen); order != 0 {
			return order
		}
		return strings.Compare(a.Fingerprint, b.Fingerprint)
	})
	return result, nil
}

func ChangeIssue(ctx context.Context, store *state.Store, home, fingerprint, status, version, note, who string) (Issue, error) {
	if status != "open" && status != "resolved" && status != "ignored" {
		return Issue{}, errors.New("invalid issue status")
	}
	if status == "resolved" && strings.TrimSpace(version) == "" {
		return Issue{}, errors.New("resolving an issue requires the fix version")
	}
	issue, err := GetIssue(ctx, home, fingerprint)
	if err != nil {
		return Issue{}, err
	}
	err = state.Transaction(ctx, store.DB, func(tx *sql.Tx) error {
		sample := ""
		if issue.Sample != nil {
			data, err := json.Marshal(issue.Sample.Sanitized())
			if err != nil {
				return err
			}
			sample = string(data)
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO issues(fingerprint,kind,title,first_seen,sample) VALUES (?,?,?,?,?)", issue.Fingerprint, issue.Kind, issue.Title, issue.FirstSeen.UnixMilli(), sample); err != nil {
			return err
		}
		if status == "resolved" {
			_, err := tx.ExecContext(ctx, "UPDATE issues SET status=?,regressed=0,resolved_at=?,resolved_by=?,resolved_version=?,note=? WHERE fingerprint=?", status, time.Now().UnixMilli(), redactString(who), redactString(version), redactString(note), issue.Fingerprint)
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE issues SET status=?,regressed=0 WHERE fingerprint=?", status, issue.Fingerprint)
		return err
	})
	if err != nil {
		return Issue{}, fmt.Errorf("change issue status: %w", err)
	}
	return GetIssue(ctx, home, issue.Fingerprint)
}
