package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/olegpustovit/agentpack/internal/environment"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
)

func (runner Runner) writeLogBundle(ctx context.Context, home, fingerprint, output string) error {
	issue, err := obs.GetIssue(ctx, home, fingerprint)
	if err != nil {
		return err
	}
	events, err := obs.IssueEvents(ctx, home, fingerprint)
	if err != nil {
		return err
	}
	nodes, sessions := map[string]bool{}, map[string]bool{}
	for _, count := range issue.Counts {
		nodes[count.Node] = true
	}
	versions := []map[string]any{}
	seen := map[string]bool{}
	for _, event := range append(slices.Clone(events), sampleEvents(issue)...) {
		nodes[event.NodeID] = true
		if event.SessionID != "" {
			sessions[event.SessionID] = true
		}
		version := map[string]any{"node_id": event.NodeID}
		for _, key := range []string{"version", "commit", "os", "arch", "schema_version", "harness", "harness_version", "ui_build_id"} {
			if value, ok := event.Attrs[key]; ok {
				version[key] = value
			}
		}
		assets := sourceAssetPattern.FindAllString(event.Stack, -1)
		if build, ok := event.Attrs["ui_build_id"].(string); ok && build != "" {
			assets = append(assets, "assets/index-"+build+".js")
		}
		slices.Sort(assets)
		version["ui_assets"] = slices.Compact(assets)
		data, err := json.Marshal(version)
		if err != nil {
			return err
		}
		if !seen[string(data)] {
			seen[string(data)] = true
			versions = append(versions, version)
		}
	}
	nodeIDs, sessionIDs := sortedIDs(nodes), sortedIDs(sessions)
	rows, err := obs.RelatedRows(ctx, home, nodeIDs, sessionIDs)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return fmt.Errorf("bundle destination already exists: %s", output)
	}
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(parent, ".agentpack-diagnostics-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	if err := paths.EnsurePrivateDirectory(temporary); err != nil {
		return err
	}
	for _, file := range []struct {
		name  string
		value any
	}{{"issue.json", issue}, {"rows.json", map[string]any{"rows": rows, "node_ids": nodeIDs, "session_ids": sessionIDs}}, {"versions.json", versions}} {
		value, err := environment.RedactDiagnosticExport(file.value)
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(temporary, file.name), append(data, '\n'), 0600); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(filepath.Join(temporary, "traces.ndjson"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	for _, event := range events {
		value, err := environment.RedactDiagnosticExport(event.Sanitized())
		if err != nil {
			return errors.Join(err, file.Close())
		}
		if err := encoder.Encode(value); err != nil {
			return errors.Join(err, file.Close())
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, output)
}

func sortedIDs(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}

func sampleEvents(issue obs.Issue) []obs.Event {
	if issue.Sample != nil {
		return []obs.Event{*issue.Sample}
	}
	return nil
}
