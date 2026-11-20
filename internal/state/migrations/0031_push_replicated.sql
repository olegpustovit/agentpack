-- Web Push keys and subscriptions replicate, secrets included, so Main can notify every device.
-- Nodes that met after each created a key pair hold several; the oldest (created_at, public_key)
-- is current. A subscription keeps the key it was made with until its browser re-subscribes.
ALTER TABLE push_keys RENAME TO push_keys_local;
ALTER TABLE push_subscriptions RENAME TO push_subscriptions_local;
DROP INDEX push_subscriptions_email;
CREATE TABLE push_keys (
    public_key TEXT NOT NULL PRIMARY KEY,
    private_key TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT 0
);
SELECT crsql_as_crr('push_keys');
CREATE TABLE push_subscriptions (
    endpoint TEXT NOT NULL PRIMARY KEY,
    email TEXT NOT NULL DEFAULT '',
    p256dh TEXT NOT NULL DEFAULT '',
    auth TEXT NOT NULL DEFAULT '',
    public_key TEXT NOT NULL DEFAULT '',
    created_node TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT 0
);
SELECT crsql_as_crr('push_subscriptions');
CREATE INDEX push_subscriptions_email ON push_subscriptions(email);
INSERT INTO push_keys(public_key,private_key,created_at)
 SELECT public_key,private_key,CAST(strftime('%s','now') AS INTEGER)*1000 FROM push_keys_local;
INSERT INTO push_subscriptions(endpoint,email,p256dh,auth,public_key,created_node,created_at)
 SELECT s.endpoint,s.email,s.p256dh,s.auth,coalesce((SELECT public_key FROM push_keys_local),''),
 (SELECT node_id FROM local_node WHERE id=1),s.created_at FROM push_subscriptions_local s;
DROP TABLE push_subscriptions_local;
DROP TABLE push_keys_local;
