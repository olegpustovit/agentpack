package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/user"
	"regexp"
	"strings"
	"time"

	"github.com/olegpustovit/agentpack/internal/network"
	"github.com/olegpustovit/agentpack/internal/obs"
	"github.com/olegpustovit/agentpack/internal/paths"
	"github.com/spf13/cobra"
)

func (runner Runner) logsCommand() *cobra.Command {
	root := &cobra.Command{Use: "logs", Short: "Inspect diagnostics and manage issues without a running gateway", SilenceErrors: true, SilenceUsage: true}
	root.SetOut(runner.Stdout)
	root.SetErr(runner.Stderr)
	var asJSON bool
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "emit JSON")
	write := func(value any) error {
		encoder := json.NewEncoder(runner.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	for _, action := range []string{"issues", "show", "trace", "query", "resolve", "reopen", "ignore", "bundle"} {
		var status, since, version, note, output string
		var allNodes bool
		filter := obs.Filter{Limit: 100}
		command := &cobra.Command{Use: action, Args: cobra.NoArgs}
		if action != "issues" && action != "query" {
			command.Use += " ID"
			command.Args = cobra.ExactArgs(1)
		}
		switch action {
		case "issues":
			command.Short = "List issues, newest first"
			command.Flags().StringVar(&status, "status", "open", "open, resolved or ignored (empty for all)")
		case "show":
			command.Short = "Show an issue, machine counts, sample and recent traces"
		case "trace":
			command.Short = "Read every retained event in one trace, in time order"
			command.Flags().BoolVar(&allNodes, "all-nodes", false, "also read every reachable mesh peer")
		case "query":
			command.Short = "Find events using structured filters; SQL is not accepted"
			command.Flags().StringVar(&since, "since", "", "duration ago (2h) or RFC3339 timestamp")
			command.Flags().StringVar(&filter.Kind, "kind", "", "event kind")
			command.Flags().StringVar(&filter.Level, "level", "", "info or error")
			command.Flags().StringVar(&filter.Session, "session", "", "session ID")
			command.Flags().StringVar(&filter.Node, "node", "", "node ID")
			command.Flags().StringVar(&filter.Trace, "trace", "", "trace ID")
			command.Flags().StringVar(&filter.Grep, "grep", "", "case-insensitive literal text")
			command.Flags().IntVar(&filter.Limit, "limit", 100, "maximum events (1 to 10000)")
		case "resolve":
			command.Short = "Record the version containing a fix"
			command.Flags().StringVar(&version, "version", "", "version containing the fix")
			command.Flags().StringVar(&note, "note", "", "resolution note")
		case "reopen":
			command.Short = "Reopen an issue"
		case "ignore":
			command.Short = "Ignore an issue"
		case "bundle":
			command.Short = "Export a redacted reproduction folder"
			command.Flags().StringVarP(&output, "output", "o", "", "new destination directory")
		}
		command.RunE = func(command *cobra.Command, args []string) error {
			if proxy, _ := command.Flags().GetBool("proxy"); proxy {
				return errors.New("--proxy is only supported with agentpack claude")
			}
			home, err := paths.UserAgentpackHome()
			if err != nil {
				return err
			}
			ctx := command.Context()
			switch action {
			case "issues":
				issues, err := obs.ListIssues(ctx, home, status)
				if err != nil {
					return err
				}
				if asJSON {
					return write(issues)
				}
				for _, issue := range issues {
					if _, err := fmt.Fprintf(runner.Stdout, "%s  %s  %d  %s  %q\n", issue.Fingerprint, issue.Status, issue.Count, issue.LastSeen.Format(time.RFC3339), issue.Title); err != nil {
						return err
					}
				}
				return nil
			case "query", "trace":
				var events []obs.Event
				if action == "trace" {
					events, err = network.TraceEvents(ctx, home, args[0], allNodes)
				} else {
					if filter.Limit == 0 {
						return errors.New("limit must be between 1 and 10000")
					}
					if since != "" {
						if elapsed, e := time.ParseDuration(since); e == nil {
							if elapsed <= 0 {
								return errors.New("since duration must be positive")
							}
							filter.Since = time.Now().Add(-elapsed)
						} else {
							filter.Since, err = time.Parse(time.RFC3339Nano, since)
							if err != nil {
								return errors.New("since must be a duration or RFC3339 timestamp")
							}
						}
					}
					events, err = obs.Query(ctx, home, filter)
				}
				if err != nil {
					return err
				}
				if asJSON {
					return write(events)
				}
				for _, event := range events {
					if _, err := fmt.Fprintf(runner.Stdout, "%s  %s  %s  node=%q  %q  trace=%s\n", event.TS.Format(time.RFC3339Nano), event.Level, event.Kind, event.NodeID, event.Name, event.TraceID); err != nil {
						return err
					}
				}
				return nil
			case "show":
				issue, err := obs.GetIssue(ctx, home, args[0])
				if err != nil {
					return err
				}
				if asJSON {
					return write(issue)
				}
				return runner.printIssue(issue)
			case "resolve", "reopen", "ignore":
				state := "open"
				if action == "resolve" {
					state = "resolved"
				} else if action == "ignore" {
					state = "ignored"
				}
				who := ""
				if current, err := user.Current(); err == nil {
					who = current.Username
				}
				issue, err := obs.ChangeIssue(ctx, home, args[0], state, version, note, who)
				if err != nil {
					return err
				}
				if asJSON {
					return write(issue)
				}
				_, err = fmt.Fprintf(runner.Stdout, "%s %s\n", issue.Fingerprint, issue.Status)
				return err
			case "bundle":
				if output == "" {
					return errors.New("bundle requires --output")
				}
				if err := runner.writeLogBundle(ctx, home, args[0], output); err != nil {
					return err
				}
				if asJSON {
					return write(map[string]string{"output": output})
				}
				_, err = fmt.Fprintln(runner.Stdout, "Wrote "+output)
				return err
			}
			return nil
		}
		root.AddCommand(command)
	}
	return root
}

var sourceAssetPattern = regexp.MustCompile(`assets/[A-Za-z0-9_.-]+\.js`)

func (runner Runner) printIssue(issue obs.Issue) error {
	if _, err := fmt.Fprintf(runner.Stdout, "%s  %s  %q\nCount: %d  First: %s  Last: %s\n", issue.Fingerprint, issue.Status, issue.Title, issue.Count, issue.FirstSeen.Format(time.RFC3339), issue.LastSeen.Format(time.RFC3339)); err != nil {
		return err
	}
	for _, count := range issue.Counts {
		if _, err := fmt.Fprintf(runner.Stdout, "  Node %q: %d, version %q, last %s\n", count.Node, count.Count, count.LastVersion, count.LastSeen.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if issue.ResolvedVersion != "" {
		if _, err := fmt.Fprintf(runner.Stdout, "Fix version: %q  By: %q  Note: %q\n", issue.ResolvedVersion, issue.ResolvedBy, issue.Note); err != nil {
			return err
		}
	}
	if issue.Sample != nil {
		if build, ok := issue.Sample.Attrs["ui_build_id"].(string); ok && build != "" {
			if _, err := fmt.Fprintf(runner.Stdout, "UI build: %q\nAsset: %q\n", build, "assets/index-"+build+".js"); err != nil {
				return err
			}
			for _, asset := range sourceAssetPattern.FindAllString(issue.Sample.Stack, -1) {
				if _, err := fmt.Fprintf(runner.Stdout, "Asset: %q\n", asset); err != nil {
					return err
				}
			}
		}
		encoder := json.NewEncoder(runner.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(issue.Sample); err != nil {
			return err
		}
	}
	if len(issue.RecentTraces) > 0 {
		_, err := fmt.Fprintln(runner.Stdout, "Recent traces: "+strings.Join(issue.RecentTraces, ", "))
		return err
	}
	return nil
}
