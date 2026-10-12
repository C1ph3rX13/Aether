package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	checkpointMaintenanceInterval = 30 * time.Second
	optimizeMaintenanceInterval   = 24 * time.Hour
	vacuumMaintenanceBudget       = 2 * time.Second
	optimizeMaintenanceBudget     = 2 * time.Second
	shutdownMaintenanceBudget     = 5 * time.Second
	vacuumPageFloor               = 256
	vacuumPageLimit               = 256
)

// MaintenanceSnapshot is the latest operational evidence produced by the
// database-owned maintenance loop. It deliberately contains no business data.
type MaintenanceSnapshot struct {
	Checkpoint         CheckpointObservation
	Vacuum             VacuumObservation
	StartupOptimize    OptimizeObservation
	Optimize           OptimizeObservation
	ShutdownCheckpoint CheckpointObservation
}

type CheckpointObservation struct {
	Mode               string
	ObservedAt         time.Time
	Duration           time.Duration
	Busy               int
	WALFrames          int
	CheckpointedFrames int
	Error              string
}

type VacuumObservation struct {
	ObservedAt     time.Time
	Duration       time.Duration
	PageCount      int
	FreelistBefore int
	FreelistAfter  int
	Threshold      int
	RequestedPages int
	ReclaimedPages int
	Deferred       bool
	Error          string
}

type OptimizeObservation struct {
	ObservedAt time.Time
	Duration   time.Duration
	Startup    bool
	Worked     bool
	Error      string
}

type databaseMaintenance struct {
	db *sql.DB

	checkpointInterval time.Duration
	optimizeInterval   time.Duration
	vacuumBudget       time.Duration
	optimizeBudget     time.Duration
	now                func() time.Time

	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	runMu       sync.Mutex
	statusMu    sync.RWMutex
	status      MaintenanceSnapshot
}

func newDatabaseMaintenance(db *sql.DB) *databaseMaintenance {
	return &databaseMaintenance{
		db: db, checkpointInterval: checkpointMaintenanceInterval,
		optimizeInterval: optimizeMaintenanceInterval,
		vacuumBudget:     vacuumMaintenanceBudget, optimizeBudget: optimizeMaintenanceBudget,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (maintenance *databaseMaintenance) start() {
	maintenance.lifecycleMu.Lock()
	defer maintenance.lifecycleMu.Unlock()
	if maintenance.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	maintenance.cancel = cancel
	maintenance.done = done
	go maintenance.loop(ctx, done)
}

func (maintenance *databaseMaintenance) stop() {
	maintenance.lifecycleMu.Lock()
	cancel, done := maintenance.cancel, maintenance.done
	maintenance.cancel, maintenance.done = nil, nil
	maintenance.lifecycleMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

func (maintenance *databaseMaintenance) loop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	checkpointTicker := time.NewTicker(maintenance.checkpointInterval)
	defer checkpointTicker.Stop()
	optimizeTicker := time.NewTicker(maintenance.optimizeInterval)
	defer optimizeTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-checkpointTicker.C:
			maintenance.runPassiveMaintenance(ctx)
		case <-optimizeTicker.C:
			if err := maintenance.runOptimize(ctx, false); err != nil {
				if ctx.Err() == nil {
					slog.Warn("SQLite planner maintenance failed", "error", err)
				}
			}
		}
	}
}

func (maintenance *databaseMaintenance) runPassiveMaintenance(ctx context.Context) (CheckpointObservation, VacuumObservation) {
	maintenance.runMu.Lock()
	defer maintenance.runMu.Unlock()
	checkpoint, checkpointErr := maintenance.checkpoint(ctx, "PASSIVE")
	maintenance.recordCheckpoint(checkpoint, false)
	if checkpointErr != nil {
		if ctx.Err() == nil {
			slog.Warn("SQLite passive checkpoint failed", "error", checkpointErr)
		}
	} else {
		slog.Debug("SQLite passive checkpoint",
			"busy", checkpoint.Busy, "wal_frames", checkpoint.WALFrames,
			"checkpointed_frames", checkpoint.CheckpointedFrames,
			"duration", checkpoint.Duration)
	}
	vacuum, vacuumErr := maintenance.incrementalVacuum(ctx)
	maintenance.recordVacuum(vacuum)
	if vacuumErr != nil && ctx.Err() == nil {
		if vacuum.Deferred {
			slog.Debug("SQLite incremental vacuum deferred", "error", vacuumErr)
		} else {
			slog.Warn("SQLite incremental vacuum failed", "error", vacuumErr)
		}
	} else if vacuum.ReclaimedPages > 0 {
		slog.Info("SQLite incremental vacuum completed",
			"requested_pages", vacuum.RequestedPages, "reclaimed_pages", vacuum.ReclaimedPages,
			"freelist_pages", vacuum.FreelistAfter, "duration", vacuum.Duration)
	}
	return checkpoint, vacuum
}

func (maintenance *databaseMaintenance) checkpoint(ctx context.Context, mode string) (CheckpointObservation, error) {
	if mode != "PASSIVE" && mode != "TRUNCATE" {
		return CheckpointObservation{}, fmt.Errorf("unsupported SQLite checkpoint mode %q", mode)
	}
	observation := CheckpointObservation{Mode: mode, ObservedAt: maintenance.now()}
	started := time.Now()
	err := maintenance.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint("+mode+")").Scan(
		&observation.Busy, &observation.WALFrames, &observation.CheckpointedFrames,
	)
	observation.Duration = time.Since(started)
	if err != nil {
		observation.Error = err.Error()
		return observation, err
	}
	return observation, nil
}

func (maintenance *databaseMaintenance) incrementalVacuum(ctx context.Context) (observation VacuumObservation, resultErr error) {
	observation = VacuumObservation{ObservedAt: maintenance.now()}
	started := time.Now()
	defer func() { observation.Duration = time.Since(started) }()
	operationCtx, cancel := context.WithTimeout(ctx, maintenance.vacuumBudget)
	defer cancel()
	if err := maintenance.db.QueryRowContext(operationCtx, "PRAGMA page_count").Scan(&observation.PageCount); err != nil {
		observation.Deferred = isSQLiteBusy(err) || operationCtx.Err() != nil
		observation.Error = err.Error()
		return observation, err
	}
	if err := maintenance.db.QueryRowContext(operationCtx, "PRAGMA freelist_count").Scan(&observation.FreelistBefore); err != nil {
		observation.Deferred = isSQLiteBusy(err) || operationCtx.Err() != nil
		observation.Error = err.Error()
		return observation, err
	}
	observation.FreelistAfter = observation.FreelistBefore
	observation.Threshold = vacuumPageThreshold(observation.PageCount)
	if observation.FreelistBefore < observation.Threshold {
		return observation, nil
	}
	observation.RequestedPages = min(vacuumPageLimit, observation.FreelistBefore)
	_, err := maintenance.db.ExecContext(operationCtx, fmt.Sprintf("PRAGMA incremental_vacuum(%d)", observation.RequestedPages))
	if err != nil {
		observation.Deferred = isSQLiteBusy(err) || operationCtx.Err() != nil
		observation.Error = err.Error()
		return observation, err
	}
	if err := maintenance.db.QueryRowContext(operationCtx, "PRAGMA freelist_count").Scan(&observation.FreelistAfter); err != nil {
		observation.Deferred = isSQLiteBusy(err) || operationCtx.Err() != nil
		observation.Error = err.Error()
		return observation, err
	}
	observation.ReclaimedPages = max(0, observation.FreelistBefore-observation.FreelistAfter)
	return observation, nil
}

func vacuumPageThreshold(pageCount int) int {
	tenPercent := (pageCount + 9) / 10
	return max(vacuumPageFloor, tenPercent)
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "sqlite_busy")
}

func (maintenance *databaseMaintenance) runStartupOptimize(ctx context.Context) error {
	return maintenance.runOptimize(ctx, true)
}

func (maintenance *databaseMaintenance) runOptimize(ctx context.Context, startup bool) error {
	maintenance.runMu.Lock()
	defer maintenance.runMu.Unlock()
	observation := OptimizeObservation{ObservedAt: maintenance.now(), Startup: startup}
	started := time.Now()
	operationCtx, cancel := context.WithTimeout(ctx, maintenance.optimizeBudget)
	defer cancel()
	statement := "PRAGMA optimize"
	if startup {
		statement = "PRAGMA optimize=0x10002"
	}
	rows, err := maintenance.db.QueryContext(operationCtx, statement)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var message sql.NullString
			if err = rows.Scan(&message); err != nil {
				break
			}
			observation.Worked = true
		}
		if err == nil {
			err = rows.Err()
		}
	}
	observation.Duration = time.Since(started)
	if err != nil {
		observation.Error = err.Error()
	}
	maintenance.recordOptimize(observation)
	if err == nil {
		slog.Debug("SQLite planner maintenance completed",
			"startup", startup, "worked", observation.Worked, "duration", observation.Duration)
	}
	return err
}

func (maintenance *databaseMaintenance) shutdown(ctx context.Context) error {
	maintenance.runMu.Lock()
	checkpoint, checkpointErr := maintenance.checkpoint(ctx, "TRUNCATE")
	if checkpointErr == nil && checkpoint.Busy != 0 {
		checkpointErr = fmt.Errorf("SQLite shutdown checkpoint remained busy")
		checkpoint.Error = checkpointErr.Error()
	}
	maintenance.recordCheckpoint(checkpoint, true)
	maintenance.runMu.Unlock()
	if checkpointErr != nil {
		slog.Warn("SQLite shutdown checkpoint failed",
			"busy", checkpoint.Busy, "wal_frames", checkpoint.WALFrames,
			"checkpointed_frames", checkpoint.CheckpointedFrames, "error", checkpointErr)
	} else {
		slog.Debug("SQLite shutdown checkpoint completed",
			"wal_frames", checkpoint.WALFrames, "checkpointed_frames", checkpoint.CheckpointedFrames,
			"duration", checkpoint.Duration)
	}
	return errors.Join(checkpointErr, maintenance.db.Close())
}

func (maintenance *databaseMaintenance) recordCheckpoint(observation CheckpointObservation, shutdown bool) {
	maintenance.statusMu.Lock()
	defer maintenance.statusMu.Unlock()
	if shutdown {
		maintenance.status.ShutdownCheckpoint = observation
	} else {
		maintenance.status.Checkpoint = observation
	}
}

func (maintenance *databaseMaintenance) recordVacuum(observation VacuumObservation) {
	maintenance.statusMu.Lock()
	maintenance.status.Vacuum = observation
	maintenance.statusMu.Unlock()
}

func (maintenance *databaseMaintenance) recordOptimize(observation OptimizeObservation) {
	maintenance.statusMu.Lock()
	if observation.Startup {
		maintenance.status.StartupOptimize = observation
	} else {
		maintenance.status.Optimize = observation
	}
	maintenance.statusMu.Unlock()
}

func (maintenance *databaseMaintenance) snapshot() MaintenanceSnapshot {
	maintenance.statusMu.RLock()
	defer maintenance.statusMu.RUnlock()
	return maintenance.status
}
