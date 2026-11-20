-- Web Push keys and browser subscriptions are machine-local, never CRRs.
CREATE TABLE push_keys (
    id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    public_key TEXT NOT NULL,
    private_key TEXT NOT NULL
);
CREATE TABLE push_subscriptions (
    endpoint TEXT NOT NULL PRIMARY KEY,
    email TEXT NOT NULL,
    p256dh TEXT NOT NULL,
    auth TEXT NOT NULL,
    created_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX push_subscriptions_email ON push_subscriptions(email);
