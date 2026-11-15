CREATE TABLE registry_objects (digest TEXT NOT NULL PRIMARY KEY,body TEXT NOT NULL DEFAULT '');
SELECT crsql_as_crr('registry_objects');
CREATE TABLE registry_names (digest TEXT NOT NULL PRIMARY KEY,body TEXT NOT NULL DEFAULT '');
SELECT crsql_as_crr('registry_names');
CREATE TABLE registry_heads (agentpack_id TEXT NOT NULL PRIMARY KEY,body TEXT NOT NULL DEFAULT '');
SELECT crsql_as_crr('registry_heads');
CREATE TABLE registry_tombstones (digest TEXT NOT NULL PRIMARY KEY,body TEXT NOT NULL DEFAULT '');
SELECT crsql_as_crr('registry_tombstones');
CREATE TABLE registry_drafts (id TEXT NOT NULL PRIMARY KEY,body TEXT NOT NULL DEFAULT '');
SELECT crsql_as_crr('registry_drafts');

-- Raw local configuration values must never be replicated.
CREATE TABLE registry_bindings (revision_id TEXT NOT NULL PRIMARY KEY,body TEXT NOT NULL);
CREATE TABLE registry_peers (node_id TEXT NOT NULL PRIMARY KEY,mesh INTEGER NOT NULL DEFAULT 0,removed INTEGER NOT NULL DEFAULT 0);
