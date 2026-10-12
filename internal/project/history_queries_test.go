package project

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestHistoryQueriesReturnsTypedDeltaEvidenceInCallerTransaction(t *testing.T) {
	service, db := newProjectTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Typed history", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
		Content: "typed evidence", ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	page, err := NewHistoryQueries().ReadPage(t.Context(), tx, created.Project.ID, HistoryPageQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.ProjectID != created.Project.ID || page.CurrentRevision != 2 || page.AvailableFromRevision != 1 ||
		len(page.Revisions) != 2 || page.Revisions[0].Revision != 2 || page.Revisions[1].Revision != 1 {
		t.Fatalf("typed History page: %+v", page)
	}
	revision := page.Revisions[0]
	if revision.Generation != 1 || revision.Kind != ChangeHintCreated || revision.Entity.Type != "hint" ||
		revision.Entity.ID == nil || *revision.Entity.ID != "h001" || revision.OccurredAt == "" ||
		revision.Source.Kind != "operator" || revision.Source.Name == "" || revision.Source.RunID != nil {
		t.Fatalf("typed History revision: %+v", revision)
	}
	evidence, ok := revision.Evidence.(HistoryTransitionEvidence)
	if !ok || evidence.Before == nil || evidence.After.Project.Revision != 2 || len(evidence.After.Hints) != 1 {
		t.Fatalf("typed transition evidence: %#v", revision.Evidence)
	}
	result, ok := evidence.Result.(HistoryAffectedRunsResult)
	if !ok || result.AffectedRunCount != 0 {
		t.Fatalf("typed result evidence: %#v", evidence.Result)
	}
}

func TestHistoryQueriesKeepsPaginationInCallerSnapshotAcrossConcurrentRevision(t *testing.T) {
	service, db := newProjectTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Snapshot pagination", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
		Content: "revision two", ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	queries := NewHistoryQueries()
	first, err := queries.ReadPage(t.Context(), tx, created.Project.ID, HistoryPageQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.CurrentRevision != 2 || len(first.Revisions) != 1 || first.Revisions[0].Revision != 2 ||
		first.NextBeforeRevision == nil || *first.NextBeforeRevision != 2 {
		t.Fatalf("first snapshot page: %+v", first)
	}

	if _, err := service.UpdateTitle(t.Context(), created.Project.ID, UpdateProjectTitleRequest{
		Title: "Committed after snapshot", ExpectedRevision: 2,
	}); err != nil {
		t.Fatal(err)
	}
	second, err := queries.ReadPage(t.Context(), tx, created.Project.ID, HistoryPageQuery{
		BeforeRevision: *first.NextBeforeRevision,
		Limit:          1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.CurrentRevision != 2 || len(second.Revisions) != 1 || second.Revisions[0].Revision != 1 ||
		second.NextBeforeRevision != nil {
		t.Fatalf("second snapshot page after concurrent Revision: %+v", second)
	}
}

func TestHistoryQueriesReadsDeltaRevisionSnapshotInCallerTransaction(t *testing.T) {
	service, db := newProjectTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Revision one", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
		Hints: []CreateHintInline{{Content: "initial guidance"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateTitle(t.Context(), created.Project.ID, UpdateProjectTitleRequest{
		Title: "Revision two", ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	queries := NewHistoryQueries()
	revisionOne, err := queries.ReadRevisionSnapshot(t.Context(), tx, created.Project.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if revisionOne.Project.Title != "Revision one" || revisionOne.Project.Revision != 1 ||
		len(revisionOne.Facts) != 2 || len(revisionOne.Hints) != 1 || len(revisionOne.Intents) != 1 {
		t.Fatalf("revision one snapshot: %+v", revisionOne)
	}
	revisionTwo, err := queries.ReadRevisionSnapshot(t.Context(), tx, created.Project.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if revisionTwo.Project.Title != "Revision two" || revisionTwo.Project.Revision != 2 ||
		len(revisionTwo.Facts) != 2 || len(revisionTwo.Hints) != 1 || len(revisionTwo.Intents) != 1 {
		t.Fatalf("revision two snapshot: %+v", revisionTwo)
	}
}

func TestHistoryQueriesReadsLegacyPageAndRevisionSnapshot(t *testing.T) {
	service, db := newProjectTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Legacy snapshot", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := marshalCanonicalProjectDetail(created)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"source":         ChangeSource{Kind: "operator", Name: "legacy operator"},
		"before":         nil,
		"after":          json.RawMessage(after),
		"result":         map[string]any{"affected_run_count": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		UPDATE blackboard_changes
		SET payload_schema = ?, source_kind = 'migration', source_name = 'Legacy history',
		    source_run_id = NULL, delta = '{}', previous_hash = NULL, change_hash = NULL, data = ?
		WHERE project_id = ? AND revision = 1`, LegacyHistorySchema, payload, created.Project.ID); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	queries := NewHistoryQueries()
	page, err := queries.ReadPage(t.Context(), tx, created.Project.ID, HistoryPageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Revisions) != 1 || page.Revisions[0].Source != (ChangeSource{Kind: "migration", Name: "Legacy history"}) {
		t.Fatalf("legacy History page: %+v", page)
	}
	if _, ok := page.Revisions[0].Evidence.(HistoryMigrationEvidence); !ok {
		t.Fatalf("legacy History evidence: %#v", page.Revisions[0].Evidence)
	}
	snapshot, err := queries.ReadRevisionSnapshot(t.Context(), tx, created.Project.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Project.ID != created.Project.ID || snapshot.Project.Title != "Legacy snapshot" ||
		snapshot.Project.Revision != 1 || len(snapshot.Facts) != 2 {
		t.Fatalf("legacy revision snapshot: %+v", snapshot)
	}
	publicSnapshot, err := service.GetBlackboardRevision(t.Context(), created.Project.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if publicSnapshot.Project != snapshot.Project || len(publicSnapshot.Facts) != len(snapshot.Facts) {
		t.Fatalf("public legacy revision snapshot: %+v", publicSnapshot)
	}
}

func TestHistoryQueriesRevisionSnapshotVerifiesChangeAndSnapshotHashes(t *testing.T) {
	t.Run("change hash", func(t *testing.T) {
		service, db := newProjectTestService(t)
		created, err := service.Create(t.Context(), CreateProjectRequest{
			Title: "Change hash", Origin: "origin", Goal: "goal",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
			Content: "revision two", ExpectedRevision: 1,
		}); err != nil {
			t.Fatal(err)
		}
		conn, err := db.GetConn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(t.Context(),
			"UPDATE blackboard_changes SET change_hash = 'tampered' WHERE project_id = ? AND revision = 2",
			created.Project.ID,
		); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		_, err = NewHistoryQueries().ReadRevisionSnapshot(t.Context(), tx, created.Project.ID, 2)
		if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
			t.Fatalf("ReadRevisionSnapshot error=%v, want change hash mismatch", err)
		}
	})

	t.Run("snapshot hash", func(t *testing.T) {
		service, db := newProjectTestService(t)
		created, err := service.Create(t.Context(), CreateProjectRequest{
			Title: "Snapshot hash", Origin: "origin", Goal: "goal",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
			Content: "revision two", ExpectedRevision: 1,
		}); err != nil {
			t.Fatal(err)
		}
		conn, err := db.GetConn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var generation int
		var kind, schema, stateJSON, createdAt string
		if err := conn.QueryRowContext(t.Context(), `
			SELECT generation, snapshot_kind, schema, state_json, created_at
			FROM project_history_snapshots WHERE project_id = ? AND revision = 1`, created.Project.ID).
			Scan(&generation, &kind, &schema, &stateJSON, &createdAt); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(t.Context(),
			"DELETE FROM project_history_snapshots WHERE project_id = ? AND revision = 1", created.Project.ID,
		); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(t.Context(), `
			INSERT INTO project_history_snapshots
			    (project_id, revision, generation, snapshot_kind, schema, state_json, state_hash, created_at)
			VALUES (?, 1, ?, ?, ?, ?, 'tampered', ?)`,
			created.Project.ID, generation, kind, schema, stateJSON, createdAt,
		); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		_, err = NewHistoryQueries().ReadRevisionSnapshot(t.Context(), tx, created.Project.ID, 2)
		if err == nil || !strings.Contains(err.Error(), "snapshot state hash mismatch") {
			t.Fatalf("ReadRevisionSnapshot error=%v, want snapshot hash mismatch", err)
		}
	})

	t.Run("snapshot schema", func(t *testing.T) {
		service, db := newProjectTestService(t)
		created, err := service.Create(t.Context(), CreateProjectRequest{
			Title: "Snapshot schema", Origin: "origin", Goal: "goal",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
			Content: "revision two", ExpectedRevision: 1,
		}); err != nil {
			t.Fatal(err)
		}
		conn, err := db.GetConn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var generation int
		var kind, stateJSON, stateHash, createdAt string
		if err := conn.QueryRowContext(t.Context(), `
			SELECT generation, snapshot_kind, state_json, state_hash, created_at
			FROM project_history_snapshots WHERE project_id = ? AND revision = 1`, created.Project.ID).
			Scan(&generation, &kind, &stateJSON, &stateHash, &createdAt); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(t.Context(),
			"DELETE FROM project_history_snapshots WHERE project_id = ? AND revision = 1", created.Project.ID,
		); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(t.Context(), `
			INSERT INTO project_history_snapshots
			    (project_id, revision, generation, snapshot_kind, schema, state_json, state_hash, created_at)
			VALUES (?, 1, ?, ?, 'project-snapshot/v2', ?, ?, ?)`,
			created.Project.ID, generation, kind, stateJSON, stateHash, createdAt,
		); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		_, err = NewHistoryQueries().ReadRevisionSnapshot(t.Context(), tx, created.Project.ID, 2)
		if err == nil || !strings.Contains(err.Error(), "snapshot schema") {
			t.Fatalf("ReadRevisionSnapshot error=%v, want unsupported snapshot schema", err)
		}
		var domainErr *DomainError
		if !errors.As(err, &domainErr) || domainErr.Code != ErrorConflict {
			t.Fatalf("ReadRevisionSnapshot error=%v, want code %s", err, ErrorConflict)
		}
	})
}

func TestHistoryQueriesRevisionSnapshotUsesPeriodicBoundaryAndHundredDeltaLimit(t *testing.T) {
	service, db := newProjectTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Revision 1", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	for revision := int64(1); revision <= 101; revision++ {
		if _, err := service.UpdateTitle(t.Context(), created.Project.ID, UpdateProjectTitleRequest{
			Title: "Revision " + fmt.Sprint(revision+1), ExpectedRevision: revision,
		}); err != nil {
			t.Fatalf("update at revision %d: %v", revision, err)
		}
	}

	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	queries := NewHistoryQueries()
	for _, revision := range []int64{100, 101, 102} {
		snapshot, readErr := queries.ReadRevisionSnapshot(t.Context(), tx, created.Project.ID, revision)
		if readErr != nil {
			t.Fatalf("read revision %d: %v", revision, readErr)
		}
		if snapshot.Project.Revision != revision || snapshot.Project.Title != "Revision "+fmt.Sprint(revision) {
			t.Fatalf("revision %d snapshot: %+v", revision, snapshot.Project)
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(),
		"DELETE FROM project_history_snapshots WHERE project_id = ? AND revision = 100", created.Project.ID,
	); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = queries.ReadRevisionSnapshot(t.Context(), tx, created.Project.ID, 102)
	if err == nil || !strings.Contains(err.Error(), "100-delta") {
		t.Fatalf("ReadRevisionSnapshot error=%v, want 100-delta bound", err)
	}
}

func TestHistoryQueriesRevisionSnapshotUsesExactPeriodicSnapshot(t *testing.T) {
	service, db := newProjectTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Revision 1", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	for revision := int64(1); revision < 100; revision++ {
		if _, err := service.UpdateTitle(t.Context(), created.Project.ID, UpdateProjectTitleRequest{
			Title: "Revision " + fmt.Sprint(revision+1), ExpectedRevision: revision,
		}); err != nil {
			t.Fatalf("update at revision %d: %v", revision, err)
		}
	}

	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(),
		"DELETE FROM project_history_snapshots WHERE project_id = ? AND revision = 1", created.Project.ID,
	); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewHistoryQueries().ReadRevisionSnapshot(t.Context(), tx, created.Project.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Project.Revision != 100 || snapshot.Project.Title != "Revision 100" {
		t.Fatalf("revision 100 snapshot: %+v", snapshot.Project)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	conn, err = db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(),
		"UPDATE blackboard_changes SET change_hash = 'tampered' WHERE project_id = ? AND revision = 100", created.Project.ID,
	); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = NewHistoryQueries().ReadRevisionSnapshot(t.Context(), tx, created.Project.ID, 100)
	if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("ReadRevisionSnapshot error=%v, want change hash mismatch", err)
	}
}

func TestHistoryQueriesReconstructsCompositeRevisionAndPeriodicSnapshot(t *testing.T) {
	service, db := newProjectTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Composite typed history", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
		Hints: []CreateHintInline{{Content: "initial guidance"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	page, err := NewHistoryQueries().ReadPage(t.Context(), tx, created.Project.ID, HistoryPageQuery{Limit: 1})
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Revisions) != 1 || page.Revisions[0].Revision != 1 {
		t.Fatalf("composite revision page: %+v", page)
	}
	composite, ok := page.Revisions[0].Evidence.(HistoryTransitionEvidence)
	if !ok || composite.Before != nil || len(composite.After.Facts) != 2 || len(composite.After.Hints) != 1 ||
		len(composite.After.Intents) != 1 || composite.After.Intents[0].Kind != IntentKindBootstrap {
		t.Fatalf("composite typed evidence: %#v", page.Revisions[0].Evidence)
	}

	for revision := int64(1); revision <= 100; revision++ {
		if _, err := service.UpdateTitle(t.Context(), created.Project.ID, UpdateProjectTitleRequest{
			Title: "Periodic " + string(rune('A'+revision%26)) + " " + fmt.Sprint(revision), ExpectedRevision: revision,
		}); err != nil {
			t.Fatalf("update at revision %d: %v", revision, err)
		}
	}
	tx, err = db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	page, err = NewHistoryQueries().ReadPage(t.Context(), tx, created.Project.ID, HistoryPageQuery{BeforeRevision: 102, Limit: 1})
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Revisions) != 1 || page.Revisions[0].Revision != 101 {
		t.Fatalf("periodic snapshot page: %+v", page)
	}
	periodic, ok := page.Revisions[0].Evidence.(HistoryTransitionEvidence)
	if !ok || periodic.Before == nil || periodic.Before.Project.Title == periodic.After.Project.Title ||
		periodic.After.Project.Revision != 101 {
		t.Fatalf("periodic typed evidence: %#v", page.Revisions[0].Evidence)
	}
}

func TestHistoryQueriesReturnsLegacyRowsAsMigrationEvidence(t *testing.T) {
	service, db := newProjectTestService(t)
	created, err := service.Create(t.Context(), CreateProjectRequest{
		Title: "Legacy evidence", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(t.Context(), `
		UPDATE blackboard_changes
		SET payload_schema = ?, source_kind = 'migration', source_name = 'Legacy history',
		    source_run_id = NULL, delta = '{}', previous_hash = NULL, change_hash = NULL,
		    data = '{"schema_version":1,"source":{"kind":"migration","name":"baseline import"},"before":null,"after":{"retained":true}}'
		WHERE project_id = ? AND revision = 1`, LegacyHistorySchema, created.Project.ID)
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	page, err := NewHistoryQueries().ReadPage(t.Context(), tx, created.Project.ID, HistoryPageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Revisions) != 1 {
		t.Fatalf("migration History page: %+v", page)
	}
	evidence, ok := page.Revisions[0].Evidence.(HistoryMigrationEvidence)
	if !ok || evidence.SchemaVersion != 1 || page.Revisions[0].Source != (ChangeSource{Kind: "migration", Name: "baseline import"}) {
		t.Fatalf("migration evidence: %#v source=%+v", page.Revisions[0].Evidence, page.Revisions[0].Source)
	}
}

func TestHistoryQueriesRejectsInvalidStorageEvidenceWithoutPartialResults(t *testing.T) {
	tests := []struct {
		name     string
		mutation string
		want     string
		wantCode ErrorCode
	}{
		{name: "missing payload schema", mutation: `UPDATE blackboard_changes SET payload_schema = '', data = '{"legacy":true}' WHERE project_id = ? AND revision = 2`, want: "unsupported", wantCode: ErrorConflict},
		{name: "future payload schema", mutation: "UPDATE blackboard_changes SET payload_schema = 'delta/v2' WHERE project_id = ? AND revision = 2", want: "unsupported", wantCode: ErrorConflict},
		{name: "legacy-looking delta envelope", mutation: `UPDATE blackboard_changes SET data = '{"legacy":"retained"}' WHERE project_id = ? AND revision = 2`, want: "invalid", wantCode: ErrorInternal},
		{name: "wrong entity type", mutation: "UPDATE blackboard_changes SET entity_type = 'project' WHERE project_id = ? AND revision = 2", want: "entity is inconsistent", wantCode: ErrorInternal},
		{name: "corrupt delta", mutation: "UPDATE blackboard_changes SET delta = '{' WHERE project_id = ? AND revision = 2", want: "invalid", wantCode: ErrorInternal},
		{name: "invalid change hash", mutation: "UPDATE blackboard_changes SET change_hash = 'tampered' WHERE project_id = ? AND revision = 2", want: "hash mismatch", wantCode: ErrorConflict},
		{name: "missing revision", mutation: "DELETE FROM blackboard_changes WHERE project_id = ? AND revision = 1", want: "hash mismatch", wantCode: ErrorConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, db := newProjectTestService(t)
			created, err := service.Create(t.Context(), CreateProjectRequest{
				Title: "Invalid history", Origin: "origin", Goal: "goal",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.CreateHint(t.Context(), created.Project.ID, CreateHintRequest{
				Content: "revision two", ExpectedRevision: 1,
			}); err != nil {
				t.Fatal(err)
			}
			conn, err := db.GetConn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(t.Context(), test.mutation, created.Project.ID); err != nil {
				conn.Close()
				t.Fatal(err)
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			page, err := NewHistoryQueries().ReadPage(t.Context(), tx, created.Project.ID, HistoryPageQuery{})
			_ = tx.Rollback()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ReadPage error=%v, want %q", err, test.want)
			}
			var domainErr *DomainError
			if !errors.As(err, &domainErr) || domainErr.Code != test.wantCode {
				t.Fatalf("ReadPage error=%v, want code %s", err, test.wantCode)
			}
			if len(page.Revisions) != 0 {
				t.Fatalf("failed read returned partial revisions: %+v", page.Revisions)
			}
		})
	}
}

func TestTypedHistoryResultRejectsNegativeAffectedRunCount(t *testing.T) {
	_, err := typedHistoryResult(HistoryDelta{
		SchemaVersion: ChangeSchemaVersion,
		Kind:          ChangeHintCreated,
		Result:        map[string]any{"affected_run_count": -1},
		Project:       &HistoryValueDelta[ProjectMeta]{ID: "proj"},
	})
	if err == nil || !strings.Contains(err.Error(), "affected Run count") {
		t.Fatalf("typedHistoryResult error=%v, want negative count rejection", err)
	}
}

func TestHistoryQueriesRejectsMissingProject(t *testing.T) {
	_, db := newProjectTestService(t)
	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = NewHistoryQueries().ReadPage(t.Context(), tx, "missing", HistoryPageQuery{})
	var domainErr *DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != ErrorNotFound {
		t.Fatalf("missing Project error=%v", err)
	}
}
