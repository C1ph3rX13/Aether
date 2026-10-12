package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// DBTX is the transaction foundation shared by business modules. It is
// satisfied by both *sql.Conn and *sql.Tx.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DB manages the SQLite database connection.
type DB struct {
	mu              sync.RWMutex
	path            string
	writer          *sql.DB
	reader          *sql.DB
	maintenance     *databaseMaintenance
	lastMaintenance MaintenanceSnapshot
}

// NewDB creates an uninitialized DB instance.
func NewDB() *DB {
	return &DB{}
}

const offlineDataRecoveryPointFilename = ".aether-offline-data-recovery-point"

func offlineDataRecoveryPointPath(databasePath string) string {
	return filepath.Join(filepath.Dir(databasePath), offlineDataRecoveryPointFilename)
}

// Configure initializes the database at the given path.
func (d *DB) Configure(path string) error {
	databaseExists, err := databaseFileHasContent(path)
	if err != nil {
		return fmt.Errorf("inspect database path: %w", err)
	}
	markerPath := offlineDataRecoveryPointPath(path)
	recoveryPoint, markerExists, err := loadOfflineDataRecoveryPoint(markerPath, path)
	if err != nil {
		return fmt.Errorf("verify offline data recovery point: %w", err)
	}
	if err := d.configure(path, recoveryPoint, databaseExists); err != nil {
		return err
	}
	if markerExists {
		if err := os.Remove(markerPath); err != nil {
			closeErr := d.Close()
			return errors.Join(fmt.Errorf("consume offline data recovery point: %w", err), closeErr)
		}
	}
	return nil
}

func databaseFileHasContent(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Size() > 0, nil
}

func (d *DB) configure(path string, recoveryPoint *verifiedOfflineDataRecoveryPoint, databaseExists bool) error {
	d.mu.RLock()
	configured := d.writer != nil
	d.mu.RUnlock()
	if configured {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create db directory: %w", err)
	}
	writer, err := sql.Open("sqlite", sqliteDSN(path, true))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	// SQLite only supports one writer at a time; keep the authoritative pool
	// serialized and let the read pool absorb bounded projection concurrency.
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	reader, err := sql.Open("sqlite", sqliteDSN(path, false))
	if err != nil {
		_ = writer.Close()
		return fmt.Errorf("open read database: %w", err)
	}
	reader.SetMaxOpenConns(readPoolSize)
	reader.SetMaxIdleConns(readPoolSize)
	migrations, err := LoadEmbeddedMigrations()
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("load database migrations: %w", err)
	}
	if err := writer.PingContext(context.Background()); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("ping write database: %w", err)
	}
	if err := assertSQLitePoolLimit(writer, 1); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("assert write SQLite pool: %w", err)
	}
	if err := assertSQLiteRuntimeBeforeMigration(context.Background(), writer); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("assert pre-migration SQLite runtime: %w", err)
	}
	if err := migrateWithStartupIntegrity(
		context.Background(), writer, migrations, CurrentSchemaVersion, recoveryPoint,
	); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("prepare database: %w", err)
	}
	if err := configureAutoVacuum(context.Background(), writer, !databaseExists || recoveryPoint != nil); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("configure auto vacuum: %w", err)
	}
	if err := assertSQLitePoolRuntime(context.Background(), writer, 1, false); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("assert write SQLite runtime: %w", err)
	}
	if err := verifyDatabaseIntegrity(context.Background(), writer); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("verify migrated database integrity: %w", err)
	}
	if err := reader.PingContext(context.Background()); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("ping read database: %w", err)
	}
	if err := assertSQLitePoolRuntime(context.Background(), reader, readPoolSize, true); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("assert read SQLite runtime: %w", err)
	}
	maintenanceDB, err := sql.Open("sqlite", sqliteDSN(path, true))
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("open maintenance database: %w", err)
	}
	maintenanceDB.SetMaxOpenConns(1)
	maintenanceDB.SetMaxIdleConns(1)
	if err := maintenanceDB.PingContext(context.Background()); err != nil {
		_ = maintenanceDB.Close()
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("ping maintenance database: %w", err)
	}
	if err := assertSQLitePoolRuntime(context.Background(), maintenanceDB, 1, false); err != nil {
		_ = maintenanceDB.Close()
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("assert maintenance SQLite runtime: %w", err)
	}
	maintenance := newDatabaseMaintenance(maintenanceDB)
	if err := maintenance.runStartupOptimize(context.Background()); err != nil {
		_ = maintenanceDB.Close()
		_ = reader.Close()
		_ = writer.Close()
		return fmt.Errorf("optimize migrated database: %w", err)
	}
	d.mu.Lock()
	d.path = path
	d.writer = writer
	d.reader = reader
	d.maintenance = maintenance
	d.lastMaintenance = MaintenanceSnapshot{}
	d.mu.Unlock()
	maintenance.start()
	return nil
}

const (
	sqliteBusyTimeoutMS = 5000
	readPoolSize        = 4
)

func sqliteDSN(path string, writer bool) string {
	txlock := "deferred"
	queryOnly := "1"
	if writer {
		txlock = "immediate"
		queryOnly = "0"
	}
	return fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)&_pragma=wal_autocheckpoint(1000)&_pragma=auto_vacuum(INCREMENTAL)&_pragma=query_only(%s)&_txlock=%s",
		path, sqliteBusyTimeoutMS, queryOnly, txlock,
	)
}

func configureAutoVacuum(ctx context.Context, db *sql.DB, allowTransition bool) error {
	var mode int
	if err := db.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return err
	}
	if mode != 2 {
		if !allowTransition {
			return fmt.Errorf("auto_vacuum=%d requires an approved offline data recovery point before switching to INCREMENTAL", mode)
		}
		if _, err := db.ExecContext(ctx, "PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
			return err
		}
	}
	if err := db.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return err
	}
	if mode != 2 {
		return fmt.Errorf("auto_vacuum=%d, want 2 (INCREMENTAL)", mode)
	}
	return nil
}

func assertSQLiteRuntime(ctx context.Context, db DBTX, readOnly bool) error {
	if err := assertSQLiteRuntimeBeforeMigration(ctx, db); err != nil {
		return err
	}
	var autoVacuum int
	if err := db.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&autoVacuum); err != nil {
		return fmt.Errorf("read auto_vacuum: %w", err)
	}
	if autoVacuum != 2 {
		return fmt.Errorf("auto_vacuum=%d, want 2 (INCREMENTAL)", autoVacuum)
	}
	var queryOnly int
	if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil {
		return fmt.Errorf("read query_only: %w", err)
	}
	wantQueryOnly := 0
	if readOnly {
		wantQueryOnly = 1
	}
	if queryOnly != wantQueryOnly {
		return fmt.Errorf("query_only=%d, want %d", queryOnly, wantQueryOnly)
	}
	return nil
}

func assertSQLiteRuntimeBeforeMigration(ctx context.Context, db DBTX) error {
	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("read journal_mode: %w", err)
	}
	if journalMode != "wal" {
		return fmt.Errorf("journal_mode=%q, want %q", journalMode, "wal")
	}
	var foreignKeys, synchronous, busyTimeout, walAutocheckpoint int
	checks := []struct {
		name string
		into *int
	}{
		{"foreign_keys", &foreignKeys},
		{"synchronous", &synchronous},
		{"busy_timeout", &busyTimeout},
		{"wal_autocheckpoint", &walAutocheckpoint},
	}
	for _, check := range checks {
		if err := db.QueryRowContext(ctx, "PRAGMA "+check.name).Scan(check.into); err != nil {
			return fmt.Errorf("read %s: %w", check.name, err)
		}
	}
	if foreignKeys != 1 {
		return fmt.Errorf("foreign_keys=%d, want 1", foreignKeys)
	}
	if synchronous != 2 {
		return fmt.Errorf("synchronous=%d, want 2 (FULL)", synchronous)
	}
	if busyTimeout != sqliteBusyTimeoutMS {
		return fmt.Errorf("busy_timeout=%d, want %d", busyTimeout, sqliteBusyTimeoutMS)
	}
	if walAutocheckpoint != 1000 {
		return fmt.Errorf("wal_autocheckpoint=%d, want 1000", walAutocheckpoint)
	}
	return nil
}

func assertSQLitePoolLimit(db *sql.DB, want int) error {
	if got := db.Stats().MaxOpenConnections; got != want {
		return fmt.Errorf("max_open_connections=%d, want %d", got, want)
	}
	return nil
}

func assertSQLitePoolRuntime(ctx context.Context, db *sql.DB, size int, readOnly bool) (err error) {
	if err := assertSQLitePoolLimit(db, size); err != nil {
		return err
	}
	connections := make([]*sql.Conn, 0, size)
	defer func() {
		for _, connection := range connections {
			err = errors.Join(err, connection.Close())
		}
	}()
	for index := 0; index < size; index++ {
		connection, connErr := db.Conn(ctx)
		if connErr != nil {
			return fmt.Errorf("open pooled connection %d: %w", index, connErr)
		}
		connections = append(connections, connection)
		if connErr := assertSQLiteRuntime(ctx, connection, readOnly); connErr != nil {
			return fmt.Errorf("assert pooled connection %d: %w", index, connErr)
		}
	}
	return nil
}

// GetConn returns a new database connection.
func (d *DB) GetConn(ctx context.Context) (*sql.Conn, error) {
	d.mu.RLock()
	writer := d.writer
	d.mu.RUnlock()
	if writer == nil {
		return nil, fmt.Errorf("database not configured")
	}
	return writer.Conn(ctx)
}

// GetReadConn returns a bounded read-only connection for projection queries.
func (d *DB) GetReadConn(ctx context.Context) (*sql.Conn, error) {
	d.mu.RLock()
	reader := d.reader
	d.mu.RUnlock()
	if reader == nil {
		return nil, fmt.Errorf("database not configured")
	}
	return reader.Conn(ctx)
}

// Ping verifies that the configured SQLite connection is currently usable.
func (d *DB) Ping(ctx context.Context) error {
	d.mu.RLock()
	writer := d.writer
	d.mu.RUnlock()
	if writer == nil {
		return fmt.Errorf("database not configured")
	}
	return writer.PingContext(ctx)
}

// PingRead verifies that a read-pool connection is usable.
func (d *DB) PingRead(ctx context.Context) error {
	d.mu.RLock()
	reader := d.reader
	d.mu.RUnlock()
	if reader == nil {
		return fmt.Errorf("database not configured")
	}
	return reader.PingContext(ctx)
}

// BeginTx starts a new database transaction.
func (d *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	d.mu.RLock()
	writer, reader := d.writer, d.reader
	d.mu.RUnlock()
	if writer == nil {
		return nil, fmt.Errorf("database not configured")
	}
	if opts != nil && opts.ReadOnly {
		if reader == nil {
			return nil, fmt.Errorf("read database not configured")
		}
		return reader.BeginTx(ctx, opts)
	}
	return writer.BeginTx(ctx, opts)
}

// Close closes the database connection.
func (d *DB) Close() error {
	d.mu.Lock()
	reader, writer, maintenance := d.reader, d.writer, d.maintenance
	d.reader, d.writer, d.maintenance = nil, nil, nil
	d.path = ""
	d.mu.Unlock()

	if maintenance != nil {
		maintenance.stop()
	}
	var closeErr error
	if reader != nil {
		closeErr = errors.Join(closeErr, reader.Close())
	}
	if writer != nil {
		closeErr = errors.Join(closeErr, writer.Close())
	}
	if maintenance != nil {
		checkpointCtx, cancel := context.WithTimeout(context.Background(), shutdownMaintenanceBudget)
		closeErr = errors.Join(closeErr, maintenance.shutdown(checkpointCtx))
		cancel()
		d.mu.Lock()
		d.lastMaintenance = maintenance.snapshot()
		d.mu.Unlock()
	}
	return closeErr
}

// MaintenanceSnapshot returns the latest bounded database-maintenance
// observations without exposing Project or Run state.
func (d *DB) MaintenanceSnapshot() MaintenanceSnapshot {
	d.mu.RLock()
	maintenance, last := d.maintenance, d.lastMaintenance
	d.mu.RUnlock()
	if maintenance == nil {
		return last
	}
	return maintenance.snapshot()
}
