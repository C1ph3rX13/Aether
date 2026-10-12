CREATE TABLE IF NOT EXISTS database_repairs (
    repair_version INTEGER PRIMARY KEY CHECK (repair_version > 0),
    repair_kind TEXT NOT NULL,
    schema_version_before INTEGER NOT NULL CHECK (schema_version_before >= 0),
    deleted_counts_json TEXT NOT NULL CHECK (json_valid(deleted_counts_json)),
    repaired_at TEXT NOT NULL
);

CREATE TRIGGER IF NOT EXISTS database_repairs_update_immutable
BEFORE UPDATE ON database_repairs
BEGIN
    SELECT RAISE(ABORT, 'database repair records are immutable');
END;

CREATE TRIGGER IF NOT EXISTS database_repairs_delete_immutable
BEFORE DELETE ON database_repairs
BEGIN
    SELECT RAISE(ABORT, 'database repair records are immutable');
END;
