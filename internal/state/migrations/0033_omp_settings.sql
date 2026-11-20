-- Network-wide Oh My Pi settings: one sparse row per setting the owner changed; value is JSON.
CREATE TABLE omp_settings (
    key TEXT NOT NULL PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);
SELECT crsql_as_crr('omp_settings');
