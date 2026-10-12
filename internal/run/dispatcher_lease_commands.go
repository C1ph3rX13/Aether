package run

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// HasActiveDispatcherLease reports whether the global Dispatcher lease is
// currently held. It deliberately exposes no ownership or fencing details.
func (s *Service) HasActiveDispatcherLease(ctx context.Context) (bool, error) {
	conn, err := s.db.GetReadConn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	var active int
	err = conn.QueryRowContext(ctx, `
		SELECT 1 FROM dispatcher_lease
		WHERE lease_name = 'global' AND lease_expires_unix_nano > ?`,
		s.clock().UnixNano(),
	).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) AcquireDispatcherLease(
	ctx context.Context,
	request DispatcherLeaseRequest,
) (DispatcherLease, error) {
	if err := request.Validate(); err != nil {
		return DispatcherLease{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DispatcherLease{}, err
	}
	defer tx.Rollback()
	now := s.clock()
	expiresAt := now.Add(request.TTL)
	result, err := tx.ExecContext(ctx, `
		INSERT INTO dispatcher_lease (
			lease_name, instance_id, fencing_token, acquired_at, renewed_at,
			lease_expires_at, lease_expires_unix_nano
		) VALUES ('global', ?, 1, ?, ?, ?, ?)
		ON CONFLICT(lease_name) DO UPDATE SET
			instance_id = excluded.instance_id,
			fencing_token = dispatcher_lease.fencing_token + 1,
			acquired_at = excluded.acquired_at,
			renewed_at = excluded.renewed_at,
			lease_expires_at = excluded.lease_expires_at,
			lease_expires_unix_nano = excluded.lease_expires_unix_nano
		WHERE dispatcher_lease.lease_expires_unix_nano <= ?`,
		request.InstanceID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		expiresAt.Format(time.RFC3339Nano), expiresAt.UnixNano(), now.UnixNano(),
	)
	if err != nil {
		return DispatcherLease{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return DispatcherLease{}, err
	}
	if rows != 1 {
		return DispatcherLease{}, &DomainError{
			Code: ErrorDispatcherLeaseHeld, Detail: "another Dispatcher owns the Docker daemon",
		}
	}
	var lease DispatcherLease
	var expires string
	if err := tx.QueryRowContext(ctx, `
		SELECT instance_id, fencing_token, lease_expires_at
		FROM dispatcher_lease WHERE lease_name = 'global'`,
	).Scan(&lease.InstanceID, &lease.FencingToken, &expires); err != nil {
		return DispatcherLease{}, err
	}
	lease.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return DispatcherLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return DispatcherLease{}, err
	}
	return lease, nil
}

func (s *Service) RenewDispatcherLease(
	ctx context.Context,
	lease DispatcherLease,
	ttl time.Duration,
) (DispatcherLease, error) {
	request := DispatcherLeaseRequest{InstanceID: lease.InstanceID, TTL: ttl}
	if err := request.Validate(); err != nil || lease.FencingToken <= 0 {
		if err != nil {
			return DispatcherLease{}, err
		}
		return DispatcherLease{}, &DomainError{Code: ErrorInvalidArgument, Detail: "Dispatcher fencing token is required"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DispatcherLease{}, err
	}
	defer tx.Rollback()
	now := s.clock()
	expiresAt := now.Add(ttl)
	result, err := tx.ExecContext(ctx, `
		UPDATE dispatcher_lease
		SET renewed_at = ?, lease_expires_at = ?, lease_expires_unix_nano = ?
		WHERE lease_name = 'global' AND instance_id = ? AND fencing_token = ?
		  AND lease_expires_unix_nano > ?`,
		now.Format(time.RFC3339Nano), expiresAt.Format(time.RFC3339Nano), expiresAt.UnixNano(),
		lease.InstanceID, lease.FencingToken, now.UnixNano(),
	)
	if err != nil {
		return DispatcherLease{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return DispatcherLease{}, err
	}
	if rows != 1 {
		return DispatcherLease{}, &DomainError{Code: ErrorFenced, Detail: "Dispatcher Lease was lost"}
	}
	lease.ExpiresAt = expiresAt
	if err := tx.Commit(); err != nil {
		return DispatcherLease{}, err
	}
	return lease, nil
}

func (s *Service) ReleaseDispatcherLease(ctx context.Context, lease DispatcherLease) error {
	if lease.InstanceID == "" || lease.FencingToken <= 0 {
		return &DomainError{Code: ErrorInvalidArgument, Detail: "Dispatcher Lease identity is invalid"}
	}
	conn, err := s.db.GetConn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	now := s.clock()
	result, err := conn.ExecContext(ctx, `
		UPDATE dispatcher_lease
		SET renewed_at = ?, lease_expires_at = ?, lease_expires_unix_nano = ?
		WHERE lease_name = 'global' AND instance_id = ? AND fencing_token = ?`,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.UnixNano(),
		lease.InstanceID, lease.FencingToken,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return &DomainError{Code: ErrorFenced, Detail: "Dispatcher Lease was lost"}
	}
	return nil
}
