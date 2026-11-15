-- crsql-alter: sessions
ALTER TABLE sessions ADD COLUMN archived_at INTEGER NOT NULL DEFAULT 0;
