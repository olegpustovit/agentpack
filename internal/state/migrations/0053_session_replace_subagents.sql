-- crsql-alter: sessions
ALTER TABLE sessions ADD COLUMN keep_native_subagents INTEGER NOT NULL DEFAULT 0;
