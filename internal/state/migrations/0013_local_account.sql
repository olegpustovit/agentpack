-- Joining and OS-account binding are machine-local authority, never CRRs.
ALTER TABLE local_node ADD COLUMN join_peer TEXT NOT NULL DEFAULT '';
ALTER TABLE local_node ADD COLUMN account_email TEXT NOT NULL DEFAULT '';
