-- crsql-alter: sessions
ALTER TABLE sessions ADD COLUMN archived_seq INTEGER NOT NULL DEFAULT 0;
