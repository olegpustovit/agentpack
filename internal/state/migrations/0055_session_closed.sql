-- crsql-alter: sessions
ALTER TABLE sessions ADD COLUMN closed_at INTEGER NOT NULL DEFAULT 0;
