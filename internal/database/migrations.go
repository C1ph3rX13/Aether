package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

const CurrentSchemaVersion = 39

const projectFirstStorageMigrationVersion = 22

const migrationTableSchema = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL,
    applied_at TEXT NOT NULL
);`

//go:embed migrations/*.sql
var embeddedMigrationFiles embed.FS

// Migration is one immutable, checksummed schema migration.
type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

type appliedMigration struct {
	Version  int
	Name     string
	Checksum string
}

// LoadEmbeddedMigrations returns the ordered migration catalog bundled with the binary.
func LoadEmbeddedMigrations() ([]Migration, error) {
	paths, err := fs.Glob(embeddedMigrationFiles, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	migrations := make([]Migration, 0, len(paths))
	for _, path := range paths {
		name := strings.TrimPrefix(path, "migrations/")
		separator := strings.IndexByte(name, '_')
		if separator <= 0 {
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
		version, err := strconv.Atoi(name[:separator])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid migration version in %q", name)
		}
		content, err := embeddedMigrationFiles.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, err)
		}
		migrations = append(migrations, NewMigration(version, name, string(content)))
	}
	if err := ValidateMigrationCatalog(migrations, CurrentSchemaVersion); err != nil {
		return nil, err
	}
	return migrations, nil
}

// NewMigration builds a checksummed migration. It is primarily useful for
// migration verification and rollback tests.
func NewMigration(version int, name, statement string) Migration {
	sum := sha256.Sum256([]byte(statement))
	return Migration{
		Version:  version,
		Name:     name,
		SQL:      statement,
		Checksum: hex.EncodeToString(sum[:]),
	}
}

// ValidateMigrationCatalog verifies that the catalog is contiguous and complete.
func ValidateMigrationCatalog(migrations []Migration, supportedVersion int) error {
	if supportedVersion < 0 {
		return fmt.Errorf("supported schema version must not be negative")
	}
	if len(migrations) < supportedVersion {
		return fmt.Errorf("migration catalog has %d files, expected %d", len(migrations), supportedVersion)
	}
	for index, migration := range migrations[:supportedVersion] {
		expected := index + 1
		if migration.Version != expected {
			return fmt.Errorf("migration catalog missing version %d", expected)
		}
	}
	return nil
}

// Migrate applies every pending migration in one transaction after verifying
// the database's recorded migration names and checksums.
func Migrate(ctx context.Context, db *sql.DB, migrations []Migration, supportedVersion int) (err error) {
	if err := ValidateMigrationCatalog(migrations, supportedVersion); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err = applyMigrations(ctx, tx, migrations, supportedVersion); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func applyMigrations(ctx context.Context, tx *sql.Tx, migrations []Migration, supportedVersion int) error {
	if _, err := tx.ExecContext(ctx, migrationTableSchema); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	applied, err := readAppliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	if err := verifyAppliedMigrations(applied, migrations, supportedVersion); err != nil {
		return err
	}

	appliedByVersion := make(map[int]struct{}, len(applied))
	for _, record := range applied {
		appliedByVersion[record.Version] = struct{}{}
	}
	for _, migration := range migrations {
		if migration.Version > supportedVersion {
			break
		}
		if _, exists := appliedByVersion[migration.Version]; exists {
			continue
		}
		var before projectFirstStorageSnapshot
		if migration.Version == projectFirstStorageMigrationVersion {
			before, err = snapshotProjectFirstStorage(ctx, tx)
			if err != nil {
				return fmt.Errorf("snapshot migration %04d input: %w", migration.Version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
			return fmt.Errorf("apply migration %04d %s: %w", migration.Version, migration.Name, err)
		}
		if migration.Version == projectFirstStorageMigrationVersion {
			after, snapshotErr := snapshotProjectFirstStorage(ctx, tx)
			if snapshotErr != nil {
				return fmt.Errorf("snapshot migration %04d output: %w", migration.Version, snapshotErr)
			}
			if err := compareProjectFirstStorageSnapshots(before, after); err != nil {
				return fmt.Errorf("verify migration %04d content: %w", migration.Version, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)",
			migration.Version, migration.Name, migration.Checksum, time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record migration %04d: %w", migration.Version, err)
		}
	}
	return nil
}

func readAppliedMigrations(ctx context.Context, db DBTX) ([]appliedMigration, error) {
	rows, err := db.QueryContext(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	defer rows.Close()

	var applied []appliedMigration
	for rows.Next() {
		var record appliedMigration
		if err := rows.Scan(&record.Version, &record.Name, &record.Checksum); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied = append(applied, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}
	return applied, nil
}

func verifyAppliedMigrations(applied []appliedMigration, migrations []Migration, supportedVersion int) error {
	if len(applied) > 0 && applied[len(applied)-1].Version > supportedVersion {
		return fmt.Errorf("database schema version %d is newer than binary supports %d", applied[len(applied)-1].Version, supportedVersion)
	}
	known := make(map[int]Migration, len(migrations))
	for _, migration := range migrations {
		known[migration.Version] = migration
	}
	for index, record := range applied {
		expectedVersion := index + 1
		if record.Version != expectedVersion {
			return fmt.Errorf("applied migration metadata is missing version %d", expectedVersion)
		}
		migration, exists := known[record.Version]
		if !exists {
			return fmt.Errorf("applied migration %d is missing from binary", record.Version)
		}
		if record.Name != migration.Name {
			return fmt.Errorf("migration %d name mismatch: database=%s binary=%s", record.Version, record.Name, migration.Name)
		}
		if record.Checksum != migration.Checksum {
			return fmt.Errorf("migration %d checksum mismatch: database=%s binary=%s", record.Version, record.Checksum, migration.Checksum)
		}
	}
	return nil
}
