package cli

import (
	"fmt"

	"github.com/olegpustovit/agentpack/internal/staging"
)

// runExtra dispatches agentpack's optional, non-core "extra" commands.
func (runner Runner) runExtra(root string, invocation Invocation) error {
	if first(invocation.Args) != "sync-claude" {
		return fmt.Errorf("extra requires an action")
	}
	if err := noArgs(invocation.Args[1:]); err != nil {
		return err
	}
	return runner.runExtraSyncClaude(root, boolFlag(invocation, "dry-run"), invocation.Global.Quiet)
}

// runExtraSyncClaude reconciles a project's .claude/skills and .agents/skills
// directories so local skills authored under either one reach both. Claude
// Code only discovers project-local skills under .claude/skills, while the
// dot-agents convention shares project-local content across every harness
// under .agents/skills.
func (runner Runner) runExtraSyncClaude(root string, dryRun, quiet bool) error {
	report, err := staging.ReconcileClaudeSkills(root, dryRun)
	if err != nil {
		return err
	}
	if quiet {
		return nil
	}
	verb := "Copied"
	if dryRun {
		verb = "Would copy"
	}
	for _, name := range report.CopiedToClaude {
		fmt.Fprintf(runner.Stdout, "%s %s: .agents/skills -> .claude/skills\n", verb, name)
	}
	for _, name := range report.CopiedToAgents {
		fmt.Fprintf(runner.Stdout, "%s %s: .claude/skills -> .agents/skills\n", verb, name)
	}
	for _, update := range report.Updated {
		direction := ".claude/skills -> .agents/skills"
		if update.Winner == "agents" {
			direction = ".agents/skills -> .claude/skills"
		}
		verbUpdate := "Reconciled"
		if dryRun {
			verbUpdate = "Would reconcile"
		}
		fmt.Fprintf(runner.Stdout, "%s %s (newer: %s): %s\n", verbUpdate, update.Name, update.Winner, direction)
	}
	total := len(report.CopiedToClaude) + len(report.CopiedToAgents) + len(report.Updated)
	if total == 0 {
		fmt.Fprintf(runner.Stdout, ".claude/skills and .agents/skills already match (%d skill(s)).\n", len(report.InSync))
		return nil
	}
	fmt.Fprintf(runner.Stdout, "Reconciled %d skill(s); %d already matched.\n", total, len(report.InSync))
	return nil
}
