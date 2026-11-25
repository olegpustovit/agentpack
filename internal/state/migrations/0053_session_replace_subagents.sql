-- crsql-alter: sessions
-- Zero preserves replacement for existing sessions; -1 keeps native subagents.
ALTER TABLE sessions ADD COLUMN replace_subagents INTEGER NOT NULL DEFAULT 0;
