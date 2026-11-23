package state

import "testing"

func TestLocalNodeHasOneUpsertThatKeepsTheAppVersion(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	if err := store.Mesh().Publish(ctx, "box", "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	// SetMain publishes the row without knowing the gateway's version.
	if err := store.SetMain(ctx, MainState{}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	nodes, err := store.Mesh().Nodes(ctx)
	if err != nil || len(nodes) != 1 {
		t.Fatal(nodes, err)
	}
	if nodes[0].AppVersion != "v1.2.3" || nodes[0].Schema != ReplicationVersion {
		t.Fatalf("%+v", nodes[0])
	}
	local, _ := store.NodeID(ctx)
	if name, err := store.Mesh().NodeName(ctx, local); err != nil || name != nodes[0].Name {
		t.Fatal(name, err)
	}
	if name, err := store.Mesh().NodeName(ctx, "unknown"); err != nil || name != "" {
		t.Fatal(name, err)
	}
	if err := store.Mesh().SetSetting(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	if value, err := store.Mesh().Setting(ctx, "k"); err != nil || value != "v" {
		t.Fatal(value, err)
	}
	if value, err := store.Mesh().Setting(ctx, "missing"); err != nil || value != "" {
		t.Fatal(value, err)
	}
}
