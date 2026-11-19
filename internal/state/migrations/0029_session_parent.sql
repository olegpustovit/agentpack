-- crsql-alter: sessions
ALTER TABLE sessions ADD COLUMN parent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN tools INTEGER NOT NULL DEFAULT 0;
