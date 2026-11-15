-- The transport uses a database-wide cursor; the extension tracks counters per author.
INSERT INTO crsql_db_versions(site_id,db_version)
SELECT crsql_site_id(),coalesce(max(db_version),0) FROM crsql_db_versions WHERE true
ON CONFLICT(site_id) DO UPDATE SET db_version=excluded.db_version
WHERE crsql_db_versions.db_version<excluded.db_version;
