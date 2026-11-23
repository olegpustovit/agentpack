package state

import (
	"encoding/json"
	"testing"
)

func TestReplicatorHooksQuarantineAndSignalChangedTables(t *testing.T) {
	RegisterValidation("hooked_", func(_ *Store, change Change) (string, error) {
		var text string
		if change.Column == "value" && json.Unmarshal(change.Value.Data, &text) == nil && text == "bad" {
			return "refused by hook", nil
		}
		return "", nil
	})
	ctx := t.Context()
	a, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, s := range []*Store{a, b} {
		if _, err := s.DB.Exec("CREATE TABLE hooked_values(id INTEGER NOT NULL PRIMARY KEY, value TEXT NOT NULL DEFAULT ''); SELECT crsql_as_crr('hooked_values')"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.DB.Exec("INSERT INTO hooked_values(id,value) VALUES (1,'good'),(2,'bad')"); err != nil {
		t.Fatal(err)
	}
	changes, next, err := a.Replicator().Changes(ctx, 0, nil, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	site, _ := a.SiteID(ctx)
	rejected, err := b.Replicator().ApplyPage(ctx, changes, site, next)
	if err != nil || len(rejected) != 1 || rejected[0].Reason != "refused by hook" {
		t.Fatalf("rejected %v %v", rejected, err)
	}
	var good string
	if err := b.Reader.QueryRow("SELECT value FROM hooked_values WHERE id=1").Scan(&good); err != nil || good != "good" {
		t.Fatal(good, err)
	}
	select {
	case <-b.Replicator().Changed():
	default:
		t.Fatal("no change signal")
	}
	if tables := b.Replicator().TakeChanged(); !tables["hooked_values"] || len(tables) != 1 {
		t.Fatal(tables)
	}
	if tables := b.Replicator().TakeChanged(); len(tables) != 0 {
		t.Fatal("tables were not drained", tables)
	}
}
