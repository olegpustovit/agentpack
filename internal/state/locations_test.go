package state

import "testing"

func TestLocalCacheLocationsFollowTheInventory(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	archive := store.Archive()
	if err := archive.SetLocalCache(ctx, map[string]CacheLocation{"k1": {Hash: "h1", Size: 1}, "k2": {Hash: "h2", Size: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec("INSERT INTO cache_locations(cache_key,node_id,content_hash,size,verified_at) VALUES ('k1','other','h1',1,0)"); err != nil {
		t.Fatal(err)
	}
	if err := archive.SetLocalCache(ctx, map[string]CacheLocation{"k1": {Hash: "h1", Size: 1}}); err != nil {
		t.Fatal(err)
	}
	if hash, err := archive.LocalCacheHash(ctx, "k2"); err != nil || hash != "" {
		t.Fatal("removed entry still advertised", hash, err)
	}
	if hash, err := archive.LocalCacheHash(ctx, "k1"); err != nil || hash != "h1" {
		t.Fatal(hash, err)
	}
	if holders, err := archive.CacheHolders(ctx, "k1", "h1"); err != nil || len(holders) != 1 || holders[0] != "other" {
		t.Fatal(holders, err)
	}
	if err := archive.AdvertiseBlob(ctx, "session-a", "sha", 3); err != nil {
		t.Fatal(err)
	}
	if session, err := archive.LocalBlobSession(ctx, "sha"); err != nil || session != "session-a" {
		t.Fatal(session, err)
	}
	if holders, err := archive.BlobHolders(ctx, "sha", "session-a"); err != nil || len(holders) != 0 {
		t.Fatal("local blob listed as a peer holder", holders, err)
	}
}
