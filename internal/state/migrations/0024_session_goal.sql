-- crsql-alter: sessions
ALTER TABLE sessions ADD COLUMN goal TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN subagents TEXT NOT NULL DEFAULT '';
