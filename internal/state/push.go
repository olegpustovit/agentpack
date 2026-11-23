package state

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Push owns the replicated Web Push key pairs and browser subscriptions.
type Push struct{ s *Store }

func (s *Store) Push() Push { return Push{s} }

// PushSubscriber is a subscription whose account may receive notifications, with the key pair it was made with.
type PushSubscriber struct {
	Endpoint, Email, P256dh, Auth, Node, Public, Private string
}

// PushFilter narrows Subscribers. Recipient applies the session-owner rule: that account, or every
// owner when the session has none. Empty fields do not filter.
type PushFilter struct {
	Endpoint, Email, Node string
	Recipient             *string
}

// currentPushKey picks the mesh's VAPID key pair. Nodes that each created one before they met
// converge on the oldest, ties broken by public key.
const currentPushKey = "SELECT public_key,private_key FROM push_keys ORDER BY created_at,public_key LIMIT 1"

// Keys returns the mesh's current VAPID key pair, creating one with generate on first use.
func (p Push) Keys(ctx context.Context, generate func() (private, public string, err error)) (public, private string, err error) {
	err = p.s.Reader.QueryRowContext(ctx, currentPushKey).Scan(&public, &private)
	if !errors.Is(err, sql.ErrNoRows) {
		return public, private, err
	}
	private, public, err = generate()
	if err != nil {
		return "", "", err
	}
	if _, err = p.s.DB.ExecContext(ctx, "INSERT INTO push_keys(public_key,private_key,created_at) SELECT ?,?,? WHERE NOT EXISTS(SELECT 1 FROM push_keys)",
		public, private, time.Now().UnixMilli()); err != nil {
		return "", "", err
	}
	// A concurrent first request may have won; everyone uses the stored pair.
	err = p.s.DB.QueryRowContext(ctx, currentPushKey).Scan(&public, &private)
	return public, private, err
}

// Subscribe records a browser subscription made through this node with the given public key.
func (p Push) Subscribe(ctx context.Context, sub PushSubscriber) error {
	_, err := p.s.DB.ExecContext(ctx, `INSERT INTO push_subscriptions(endpoint,email,p256dh,auth,public_key,created_node,created_at) VALUES (?,?,?,?,?,?,?)
 ON CONFLICT(endpoint) DO UPDATE SET email=excluded.email,p256dh=excluded.p256dh,auth=excluded.auth,public_key=excluded.public_key,created_node=excluded.created_node,created_at=excluded.created_at`,
		sub.Endpoint, sub.Email, sub.P256dh, sub.Auth, sub.Public, sub.Node, time.Now().UnixMilli())
	return err
}

// Unsubscribe removes an endpoint; a non-empty email limits it to that account's subscription.
func (p Push) Unsubscribe(ctx context.Context, endpoint, email string) error {
	_, err := p.s.DB.ExecContext(ctx, "DELETE FROM push_subscriptions WHERE endpoint=? AND (?='' OR email=?)", endpoint, email, email)
	return err
}

// Subscribed reports whether any subscription exists, limited to node when it is not empty.
func (p Push) Subscribed(ctx context.Context, node string) (bool, error) {
	var subscribed bool
	err := p.s.Reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM push_subscriptions WHERE ?='' OR created_node=?)", node, node).Scan(&subscribed)
	return subscribed, err
}

// Subscribers lists subscriptions whose account is enabled and that were created after the
// account's last sign-out everywhere or password change.
func (p Push) Subscribers(ctx context.Context, filter PushFilter) ([]PushSubscriber, error) {
	recipient, anyRecipient := "", filter.Recipient == nil
	if !anyRecipient {
		recipient = *filter.Recipient
	}
	rows, err := p.s.Reader.QueryContext(ctx, `SELECT s.endpoint,s.email,s.p256dh,s.auth,s.created_node,k.public_key,k.private_key FROM push_subscriptions s
 JOIN users u ON u.email=s.email JOIN push_keys k ON k.public_key=s.public_key
 WHERE u.disabled=0 AND s.created_at>=u.sessions_valid_after
 AND (?='' OR s.endpoint=?) AND (?='' OR s.email=?) AND (?='' OR s.created_node=?)
 AND (? OR s.email=? OR (?='' AND u.role='owner'))`,
		filter.Endpoint, filter.Endpoint, filter.Email, filter.Email, filter.Node, filter.Node, anyRecipient, recipient, recipient)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PushSubscriber
	for rows.Next() {
		var sub PushSubscriber
		if err := rows.Scan(&sub.Endpoint, &sub.Email, &sub.P256dh, &sub.Auth, &sub.Node, &sub.Public, &sub.Private); err != nil {
			return nil, err
		}
		result = append(result, sub)
	}
	return result, rows.Err()
}
