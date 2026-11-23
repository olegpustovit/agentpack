package cli

import (
	"fmt"

	"github.com/olegpustovit/agentpack/internal/environment"
)

func (runner Runner) runSupport(workspace string, invocation Invocation) (int, error) {
	if first(invocation.Args) != "export" {
		return 2, fmt.Errorf("support requires an action (export)")
	}
	return runner.supportExport(workspace, invocation.Args[1:], stringFlag(invocation, "output"), boolFlag(invocation, "json"), invocation.Global.Quiet)
}

func (runner Runner) supportExport(workspace string, args []string, output string, jsonOut, quiet bool) (int, error) {
	if len(args) != 1 {
		return 2, fmt.Errorf("support export requires RECEIPT_ID")
	}
	if output == "" {
		return 2, fmt.Errorf("support export requires --output PATH")
	}
	receipt, err := environment.LoadReceipt(workspace, args[0])
	if err != nil {
		return 5, err
	}
	capsule := environment.ExportSupportCapsule(receipt)
	if err := environment.WriteSupportCapsule(output, capsule); err != nil {
		return 1, err
	}
	if jsonOut {
		if err := writeJSON(runner.Stdout, capsule); err != nil {
			return 1, err
		}
	} else if !quiet {
		fmt.Fprintf(runner.Stdout, "Wrote redacted support capsule to %s\n", output)
		fmt.Fprintln(runner.Stdout, "Preview scope:", capsule.Scope)
		for _, note := range capsule.Notes {
			fmt.Fprintf(runner.Stdout, "note\t%s\n", note)
		}
	}
	return 0, nil
}
