package run_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
	"github.com/C1ph3rX13/Aether/internal/run"
)

func TestServiceOwnsDispatcherLeaseLifecycle(t *testing.T) {
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "aether.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})

	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	service := run.NewService(db, nil, nil, run.ServiceOptions{
		Clock: func() time.Time { return now },
	})
	request := run.DispatcherLeaseRequest{InstanceID: "dispatcher-a", TTL: 30 * time.Second}
	lease, err := service.AcquireDispatcherLease(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if lease.FencingToken != 1 || !lease.ExpiresAt.Equal(now.Add(request.TTL)) {
		t.Fatalf("acquired lease: %+v", lease)
	}

	_, err = service.AcquireDispatcherLease(context.Background(), run.DispatcherLeaseRequest{
		InstanceID: "dispatcher-b", TTL: request.TTL,
	})
	assertRunServiceErrorCode(t, err, run.ErrorDispatcherLeaseHeld)

	now = now.Add(5 * time.Second)
	renewed, err := service.RenewDispatcherLease(context.Background(), lease, request.TTL)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.FencingToken != lease.FencingToken || !renewed.ExpiresAt.Equal(now.Add(request.TTL)) {
		t.Fatalf("renewed lease: %+v", renewed)
	}

	now = renewed.ExpiresAt
	replacement, err := service.AcquireDispatcherLease(context.Background(), run.DispatcherLeaseRequest{
		InstanceID: "dispatcher-b", TTL: request.TTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.FencingToken != lease.FencingToken+1 {
		t.Fatalf("replacement fencing token=%d, want %d", replacement.FencingToken, lease.FencingToken+1)
	}
	_, err = service.RenewDispatcherLease(context.Background(), renewed, request.TTL)
	assertRunServiceErrorCode(t, err, run.ErrorFenced)
	if err := service.ReleaseDispatcherLease(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}

	third, err := service.AcquireDispatcherLease(context.Background(), run.DispatcherLeaseRequest{
		InstanceID: "dispatcher-c", TTL: request.TTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if third.FencingToken != replacement.FencingToken+1 {
		t.Fatalf("released lease reset fencing token: replacement=%+v third=%+v", replacement, third)
	}
}

func TestServiceReportsOnlyUnexpiredDispatcherLeaseAsActive(t *testing.T) {
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "aether.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})

	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	service := run.NewService(db, nil, nil, run.ServiceOptions{Clock: func() time.Time { return now }})
	active, err := service.HasActiveDispatcherLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("missing Dispatcher Lease reported active")
	}

	lease, err := service.AcquireDispatcherLease(t.Context(), run.DispatcherLeaseRequest{
		InstanceID: "dispatcher-readiness",
		TTL:        30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err = service.HasActiveDispatcherLease(t.Context())
	if err != nil || !active {
		t.Fatalf("active Dispatcher Lease=%t, error=%v", active, err)
	}

	now = lease.ExpiresAt
	active, err = service.HasActiveDispatcherLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("expired Dispatcher Lease reported active")
	}
}

func assertRunServiceErrorCode(t *testing.T, err error, want run.ErrorCode) {
	t.Helper()
	var domainErr *run.DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != want {
		t.Fatalf("error=%v, want code %s", err, want)
	}
}
