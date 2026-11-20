-- crsql-alter: sessions
ALTER TABLE sessions ADD COLUMN worktree_of TEXT NOT NULL DEFAULT '';
