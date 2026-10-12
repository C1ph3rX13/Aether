package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// ProviderReadiness records optional provider/API health without probing the runtime image.
type ProviderReadiness struct {
	mu         sync.RWMutex
	ready      map[providerReadinessKey]struct{}
	lastUsed   map[providerReadinessKey]uint64
	accessSeq  uint64
	refreshGen uint64
}

const (
	maxProviderReadinessEntries     = 256
	maxProviderReadinessParallelism = 8
)

type providerReadinessKey struct {
	configRevision string
	workerName     string
}

type ProviderHealthChecker interface {
	CheckProvider(context.Context, CatalogWorker) error
}

type ProviderHealthCheckerFunc func(context.Context, CatalogWorker) error

func (check ProviderHealthCheckerFunc) CheckProvider(ctx context.Context, worker CatalogWorker) error {
	return check(ctx, worker)
}

func NewProviderReadiness() *ProviderReadiness {
	return &ProviderReadiness{
		ready: make(map[providerReadinessKey]struct{}), lastUsed: make(map[providerReadinessKey]uint64),
	}
}

func (readiness *ProviderReadiness) MarkReady(configRevision string, workerName string) {
	if readiness == nil || configRevision == "" || workerName == "" {
		return
	}
	readiness.mu.Lock()
	readiness.refreshGen++
	key := providerReadinessKey{configRevision: configRevision, workerName: workerName}
	readiness.ready[key] = struct{}{}
	readiness.touchLocked(key)
	readiness.pruneLocked()
	readiness.mu.Unlock()
}

func (readiness *ProviderReadiness) MarkNotReady(configRevision string, workerName string) {
	if readiness == nil {
		return
	}
	readiness.mu.Lock()
	readiness.refreshGen++
	key := providerReadinessKey{configRevision: configRevision, workerName: workerName}
	delete(readiness.ready, key)
	delete(readiness.lastUsed, key)
	readiness.mu.Unlock()
}

// Refresh is the producer path for Provider readiness. The checker receives an
// immutable Worker copy and owns any transient credential resolution it needs.
func (readiness *ProviderReadiness) Refresh(ctx context.Context, catalog *Catalog, checker ProviderHealthChecker) error {
	if readiness == nil || catalog == nil || checker == nil {
		return fmt.Errorf("Provider readiness refresh dependencies are required")
	}
	readiness.mu.Lock()
	readiness.refreshGen++
	generation := readiness.refreshGen
	for key := range readiness.ready {
		delete(readiness.ready, key)
		delete(readiness.lastUsed, key)
	}
	readiness.mu.Unlock()
	workers := catalog.Workers()
	results := make([]error, len(workers))
	jobs := make(chan int)
	parallelism := min(len(workers), maxProviderReadinessParallelism)
	var checks sync.WaitGroup
	checks.Add(parallelism)
	for range parallelism {
		go func() {
			defer checks.Done()
			for index := range jobs {
				if err := ctx.Err(); err != nil {
					results[index] = err
					continue
				}
				results[index] = checker.CheckProvider(ctx, workers[index])
			}
		}()
	}
	for index := range workers {
		jobs <- index
	}
	close(jobs)
	checks.Wait()

	var failures []error
	contextErr := ctx.Err()
	for index, catalogWorker := range workers {
		if err := results[index]; err != nil {
			if !readiness.setRefreshResult(generation, catalog.Revision(), catalogWorker.Name, false) {
				return fmt.Errorf("Provider readiness refresh was superseded")
			}
			if contextErr == nil || !errors.Is(err, contextErr) {
				failures = append(failures, fmt.Errorf("Worker %s Provider healthcheck: %w", catalogWorker.Name, err))
			}
			continue
		}
		if !readiness.setRefreshResult(generation, catalog.Revision(), catalogWorker.Name, true) {
			return fmt.Errorf("Provider readiness refresh was superseded")
		}
	}
	if contextErr != nil {
		failures = append(failures, contextErr)
	}
	return errors.Join(failures...)
}

func (readiness *ProviderReadiness) setRefreshResult(generation uint64, configRevision, workerName string, ready bool) bool {
	readiness.mu.Lock()
	defer readiness.mu.Unlock()
	if generation != readiness.refreshGen {
		return false
	}
	key := providerReadinessKey{configRevision: configRevision, workerName: workerName}
	if ready {
		readiness.ready[key] = struct{}{}
		readiness.touchLocked(key)
		readiness.pruneLocked()
	} else {
		delete(readiness.ready, key)
		delete(readiness.lastUsed, key)
	}
	return true
}

// IsReady reports the current Provider readiness for one immutable Worker
// Catalog revision.
func (readiness *ProviderReadiness) IsReady(configRevision, workerName string) bool {
	if readiness == nil || configRevision == "" || workerName == "" {
		return false
	}
	readiness.mu.Lock()
	key := providerReadinessKey{configRevision: configRevision, workerName: workerName}
	_, ok := readiness.ready[key]
	if ok {
		readiness.touchLocked(key)
	}
	readiness.mu.Unlock()
	return ok
}

func (readiness *ProviderReadiness) CheckStartReadiness(_ context.Context, request rundomain.StartRequest) error {
	if !readiness.IsReady(request.Spec.ConfigRevision, request.Spec.Worker.Name) {
		return &rundomain.DomainError{Code: rundomain.ErrorNotReady, Detail: "Worker provider is not ready"}
	}
	return nil
}

func (readiness *ProviderReadiness) touchLocked(key providerReadinessKey) {
	readiness.accessSeq++
	readiness.lastUsed[key] = readiness.accessSeq
}

func (readiness *ProviderReadiness) pruneLocked() {
	for len(readiness.ready) > maxProviderReadinessEntries {
		var oldest providerReadinessKey
		var oldestAccess uint64
		first := true
		for key := range readiness.ready {
			access := readiness.lastUsed[key]
			if first || access < oldestAccess {
				oldest, oldestAccess, first = key, access, false
			}
		}
		delete(readiness.ready, oldest)
		delete(readiness.lastUsed, oldest)
	}
}

var _ rundomain.StartReadiness = (*ProviderReadiness)(nil)
