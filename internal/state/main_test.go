package state

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestMainSwitchPausesArchiveUntilValidatedAndConverges(t *testing.T) {
	ctx := t.Context()
	homeA, homeB := t.TempDir(), t.TempDir()
	a, err := Open(homeA)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(homeB)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := a.BindNode(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := b.BindNode(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetMain(ctx, MainState{}, homeA); err != nil {
		t.Fatal(err)
	}
	sync := func(from, to *Store) {
		t.Helper()
		site, err := from.SiteID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cursor, err := to.Cursor(ctx, site)
		if err != nil {
			t.Fatal(err)
		}
		rows, next, err := from.Changes(ctx, cursor, nil, 4<<20)
		if err != nil {
			t.Fatal(err)
		}
		if err := to.ApplyPage(ctx, rows, site, next); err != nil {
			t.Fatal(err)
		}
	}
	sync(a, b)
	roles, err := b.Main(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if roles.MainNode != "a" || roles.ArchiveNode != "a" {
		t.Fatalf("first main: %+v", roles)
	}
	if err := b.SetMain(ctx, roles, homeB); err != nil {
		t.Fatal(err)
	}
	sync(b, a)
	for _, store := range []*Store{a, b} {
		roles, err := store.Main(ctx)
		if err != nil || roles.MainNode != "b" || roles.ArchiveNode != "a" {
			t.Fatalf("switch did not pause: %+v %v", roles, err)
		}
		var count int
		if err := store.Reader.QueryRow("SELECT count(*) FROM mesh_settings WHERE key='main_node'").Scan(&count); err != nil || count != 1 {
			t.Fatalf("not exactly one main setting: %d %v", count, err)
		}
	}
	pending, _ := b.Main(ctx)
	if err := b.SetMain(ctx, pending, homeB); err != nil {
		t.Fatal(err)
	}
	stillPending, _ := b.Main(ctx)
	if stillPending.ArchiveNode != "a" {
		t.Fatal("repeated main set bypassed handover")
	}
	if err := b.ArchiveReady(ctx, homeB, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing archive accepted: %v", err)
	}
	if err := a.ArchiveReady(ctx, homeA, true); err == nil {
		t.Fatal("non-main prepared an archive")
	}
	if err := b.ArchiveReady(ctx, homeB, true); err != nil {
		t.Fatal(err)
	}
	if err := b.SetMain(ctx, pending, homeB); !errors.Is(err, ErrMainChanged) {
		t.Fatalf("stale archive confirmation accepted: %v", err)
	}
	sync(b, a)
	ready, err := a.Main(ctx)
	if err != nil || ready.MainNode != "b" || ready.ArchiveNode != "b" {
		t.Fatalf("archive ready did not converge: %+v %v", ready, err)
	}
}

func TestOfflineMainRoleFollowsStableNodeBinding(t *testing.T) {
	home := t.TempDir()
	s, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetMain(t.Context(), MainState{}, home); err != nil {
		t.Fatal(err)
	}
	if err := s.BindNode(t.Context(), "stable"); err != nil {
		t.Fatal(err)
	}
	roles, err := s.Main(t.Context())
	if err != nil || roles.MainNode != "stable" || roles.ArchiveNode != "stable" {
		t.Fatalf("binding orphaned main role: %+v %v", roles, err)
	}
}

func TestArchiveReadyRequiresCompleteHistory(t *testing.T) {
	home := t.TempDir()
	store, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	if err := store.SetMain(ctx, MainState{}, home); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec("INSERT INTO sessions(id,archived_at,archived_seq) VALUES ('short-history',1,2)"); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveReady(ctx, home, true); err == nil {
		t.Fatal("accepted an empty archive with archived sessions")
	}
	archive, err := OpenArchive(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Exec("INSERT INTO conversation_events VALUES ('short-history',1,'{}')"); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveReady(ctx, home, false); err == nil || !strings.Contains(err.Error(), "short-history") {
		t.Fatalf("accepted or omitted the short session: %v", err)
	}
	if _, err := archive.Exec("INSERT INTO conversation_events VALUES ('short-history',2,'{}')"); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.ArchiveReady(ctx, home, false); err != nil {
		t.Fatal(err)
	}
}
