-- crsql-alter: sessions
ALTER TABLE sessions ADD COLUMN waiting TEXT NOT NULL DEFAULT '';
