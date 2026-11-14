package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/olegpustovit/agentpack/internal/state"
	"github.com/spf13/cobra"
)

func runStateSelfcheck(command *cobra.Command, _ []string) (err error) {
	dir, err := os.MkdirTemp("", "agentpack-state-selfcheck-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	store, err := state.Open(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	if _, err := store.DB.ExecContext(command.Context(), `
 CREATE TABLE selfcheck (id INTEGER NOT NULL PRIMARY KEY, body TEXT NOT NULL DEFAULT '');
 SELECT crsql_as_crr('selfcheck');
 INSERT INTO selfcheck VALUES (1, 'ok');`); err != nil {
		return err
	}
	var value string
	if err := store.DB.QueryRowContext(command.Context(), `SELECT val FROM crsql_changes WHERE "table" = 'selfcheck' AND cid = 'body'`).Scan(&value); err != nil {
		return err
	}
	if value != "ok" {
		return fmt.Errorf("state self-check: replicated value %q, want ok", value)
	}
	_, err = fmt.Fprintln(command.OutOrStdout(), "ok")
	return err
}
