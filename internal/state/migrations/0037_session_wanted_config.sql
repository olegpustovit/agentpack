-- crsql-alter: sessions
ALTER TABLE sessions ADD COLUMN wanted_config TEXT NOT NULL DEFAULT '{}';
