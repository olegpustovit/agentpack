package state

import "testing"

func TestPushSubscribersFollowRecipientAndNode(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	if _, err := store.DB.Exec("INSERT INTO users(email,password_hash,role) VALUES ('owner@x','h','owner'),('member@x','h','member')"); err != nil {
		t.Fatal(err)
	}
	public, _, err := store.Push().Keys(ctx, func() (string, string, error) { return "private", "public", nil })
	if err != nil || public != "public" {
		t.Fatal(public, err)
	}
	// A second generator never replaces the stored pair.
	if again, _, err := store.Push().Keys(ctx, func() (string, string, error) { return "p2", "k2", nil }); err != nil || again != "public" {
		t.Fatal(again, err)
	}
	for _, sub := range []PushSubscriber{{Endpoint: "e1", Email: "owner@x", Node: "n1"}, {Endpoint: "e2", Email: "member@x", Node: "n2"}} {
		sub.P256dh, sub.Auth, sub.Public = "p", "a", public
		if err := store.Push().Subscribe(ctx, sub); err != nil {
			t.Fatal(err)
		}
	}
	count := func(filter PushFilter) int {
		subs, err := store.Push().Subscribers(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		return len(subs)
	}
	none, member := "", "member@x"
	if got := count(PushFilter{}); got != 2 {
		t.Fatal("all", got)
	}
	if got := count(PushFilter{Recipient: &none}); got != 1 {
		t.Fatal("ownerless session reaches owners only", got)
	}
	if got := count(PushFilter{Recipient: &member, Node: "n1"}); got != 0 {
		t.Fatal("node scope", got)
	}
	if ok, err := store.Push().Subscribed(ctx, "n3"); err != nil || ok {
		t.Fatal("n3 subscribed", ok, err)
	}
	if err := store.Push().Unsubscribe(ctx, "e2", "owner@x"); err != nil || count(PushFilter{}) != 2 {
		t.Fatal("another account removed a subscription", err)
	}
	if err := store.Push().Unsubscribe(ctx, "e2", ""); err != nil || count(PushFilter{}) != 1 {
		t.Fatal("forget endpoint", err)
	}
}
