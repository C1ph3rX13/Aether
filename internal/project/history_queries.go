package project

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	defaultHistoryPageLimit = 50
	maximumHistoryPageLimit = 100
)

// HistoryQueries is Project History's unified query interface for normalized
// revision pages and revision snapshots. Callers supply the transaction so
// History can participate in a larger consistent read.
type HistoryQueries struct{}

func NewHistoryQueries() *HistoryQueries {
	return &HistoryQueries{}
}

type HistoryPageQuery struct {
	BeforeRevision int64
	Limit          int
}

type HistoryEntity struct {
	Type string
	ID   *string
}

// HistoryRevision is normalized domain evidence. Storage schemas, raw
// envelopes, deltas, snapshots, and hashes do not cross this seam.
type HistoryRevision struct {
	ProjectID  string
	Revision   int64
	Generation int
	Kind       ChangeKind
	Entity     HistoryEntity
	Source     ChangeSource
	OccurredAt string
	Evidence   HistoryEvidence
}

type HistoryEvidence interface {
	historyEvidence()
}

type HistoryMigrationEvidence struct {
	SchemaVersion int
	snapshot      *ProjectDetail
}

type legacyHistoryPayload struct {
	SchemaVersion int             `json:"schema_version"`
	Source        ChangeSource    `json:"source"`
	Before        json.RawMessage `json:"before"`
	After         json.RawMessage `json:"after"`
	Result        json.RawMessage `json:"result,omitempty"`
}

func (HistoryMigrationEvidence) historyEvidence() {}

type HistoryTransitionEvidence struct {
	Before *ProjectDetail
	After  ProjectDetail
	Result HistoryResultEvidence
}

func (HistoryTransitionEvidence) historyEvidence() {}

type HistoryResultEvidence interface {
	historyResultEvidence()
}

type HistoryNoResult struct{}

func (HistoryNoResult) historyResultEvidence() {}

type HistoryAffectedRunsResult struct {
	AffectedRunCount int
}

func (HistoryAffectedRunsResult) historyResultEvidence() {}

type HistoryReasonResult struct {
	Reason           string
	AffectedRunCount int
}

func (HistoryReasonResult) historyResultEvidence() {}

type HistorySupersedeResult struct {
	Reason            string
	ReplacementFactID string
	AffectedRunCount  int
}

func (HistorySupersedeResult) historyResultEvidence() {}

type HistoryProposedIntentsResult struct {
	IntentIDs []string
}

func (HistoryProposedIntentsResult) historyResultEvidence() {}

type HistoryConcludedIntentResult struct {
	FactID string
}

func (HistoryConcludedIntentResult) historyResultEvidence() {}

type HistoryCompletedProjectResult struct {
	CompletionIntentID string
	AffectedRunCount   *int
}

func (HistoryCompletedProjectResult) historyResultEvidence() {}

type HistoryReopenedProjectResult struct {
	HintID             string
	PreviousGeneration int
}

func (HistoryReopenedProjectResult) historyResultEvidence() {}

type HistoryBootstrapCompletedResult struct {
	FactID             string
	TargetIntentID     string
	CompletionIntentID string
}

func (HistoryBootstrapCompletedResult) historyResultEvidence() {}

type HistoryPage struct {
	ProjectID                 string
	AvailableFromRevision     int64
	AvailableFromOccurredAt   string
	CurrentRevision           int64
	CurrentRevisionOccurredAt string
	Revisions                 []HistoryRevision
	NextBeforeRevision        *int64
}

func (queries *HistoryQueries) ReadRevisionSnapshot(
	ctx context.Context,
	conn DBTX,
	projectID string,
	revision int64,
) (ProjectDetail, error) {
	if revision < 1 {
		return ProjectDetail{}, InvalidArgument("revision must be positive")
	}
	change, err := new(historyChangeStore).GetRevision(ctx, conn, projectID, revision)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectDetail{}, NotFound("Revision not found")
	}
	if err != nil {
		return ProjectDetail{}, historyQueryError(err)
	}
	snapshot, found, err := historySnapshotAtRevision(ctx, conn, projectID, revision)
	if err != nil {
		return ProjectDetail{}, historyQueryError(err)
	}
	if found {
		if validationErr := queries.validateRevisionForSnapshot(ctx, conn, change); validationErr != nil {
			return ProjectDetail{}, historyQueryError(validationErr)
		}
		detail, decodeErr := decodeHistorySnapshot(projectID, snapshot)
		if decodeErr != nil {
			return ProjectDetail{}, historyQueryError(decodeErr)
		}
		return detail, nil
	}
	normalized, err := queries.normalizeRevision(ctx, conn, change)
	if err != nil {
		return ProjectDetail{}, historyQueryError(err)
	}
	switch evidence := normalized.Evidence.(type) {
	case HistoryTransitionEvidence:
		return evidence.After, nil
	case HistoryMigrationEvidence:
		if evidence.snapshot == nil {
			return ProjectDetail{}, Conflict("revision has no complete Blackboard after-image")
		}
		return *evidence.snapshot, nil
	default:
		return ProjectDetail{}, Internal("History revision evidence is incomplete")
	}
}

func (queries *HistoryQueries) ReadPage(
	ctx context.Context,
	conn DBTX,
	projectID string,
	query HistoryPageQuery,
) (HistoryPage, error) {
	if query.BeforeRevision < 0 {
		return HistoryPage{}, InvalidArgument("before_revision must not be negative")
	}
	if query.Limit == 0 {
		query.Limit = defaultHistoryPageLimit
	}
	if query.Limit < 1 || query.Limit > maximumHistoryPageLimit {
		return HistoryPage{}, InvalidArgument("limit must be between 1 and 100")
	}

	page, err := readHistoryRange(ctx, conn, projectID)
	if err != nil {
		return HistoryPage{}, err
	}
	if page.CurrentRevision == 0 {
		return page, nil
	}
	beforeRevision := query.BeforeRevision
	if beforeRevision == 0 || beforeRevision > page.CurrentRevision+1 {
		beforeRevision = page.CurrentRevision + 1
	}
	rows, err := conn.QueryContext(ctx, `
		SELECT project_id, revision, generation, kind, entity_type, entity_id, data,
		       payload_schema, source_kind, source_name, source_run_id, delta,
		       previous_hash, change_hash, created_at
		FROM blackboard_changes
		WHERE project_id = ? AND revision < ?
		ORDER BY revision DESC
		LIMIT ?`, projectID, beforeRevision, query.Limit+1)
	if err != nil {
		return HistoryPage{}, err
	}
	changes := make([]BlackboardChange, 0, query.Limit+1)
	for rows.Next() {
		change, scanErr := scanBlackboardChange(rows)
		if scanErr != nil {
			rows.Close()
			return HistoryPage{}, historyQueryError(scanErr)
		}
		changes = append(changes, change)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return HistoryPage{}, err
	}
	if err := rows.Close(); err != nil {
		return HistoryPage{}, err
	}
	if len(changes) > query.Limit {
		changes = changes[:query.Limit]
		next := changes[len(changes)-1].Revision
		page.NextBeforeRevision = &next
	}

	revisions := make([]HistoryRevision, 0, len(changes))
	for _, change := range changes {
		revision, readErr := queries.normalizeRevision(ctx, conn, change)
		if readErr != nil {
			return HistoryPage{}, historyQueryError(readErr)
		}
		revisions = append(revisions, revision)
	}
	page.Revisions = revisions
	return page, nil
}

func readHistoryRange(ctx context.Context, conn DBTX, projectID string) (HistoryPage, error) {
	page := HistoryPage{ProjectID: projectID, Revisions: []HistoryRevision{}}
	if err := conn.QueryRowContext(ctx,
		"SELECT blackboard_revision FROM projects WHERE id = ?", projectID,
	).Scan(&page.CurrentRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return HistoryPage{}, NotFound("Project not found")
		}
		return HistoryPage{}, err
	}
	var firstRevision, lastRevision sql.NullInt64
	var firstOccurredAt, lastOccurredAt sql.NullString
	var count int64
	if err := conn.QueryRowContext(ctx, `
		SELECT
			MIN(revision),
			(SELECT created_at FROM blackboard_changes WHERE project_id = ? ORDER BY revision ASC LIMIT 1),
			MAX(revision),
			(SELECT created_at FROM blackboard_changes WHERE project_id = ? ORDER BY revision DESC LIMIT 1),
			COUNT(*)
		FROM blackboard_changes WHERE project_id = ?`, projectID, projectID, projectID,
	).Scan(&firstRevision, &firstOccurredAt, &lastRevision, &lastOccurredAt, &count); err != nil {
		return HistoryPage{}, err
	}
	if !firstRevision.Valid {
		if page.CurrentRevision != 0 {
			return HistoryPage{}, Internal("Project history is incomplete")
		}
		return page, nil
	}
	if !firstOccurredAt.Valid || !lastRevision.Valid || !lastOccurredAt.Valid ||
		lastRevision.Int64 != page.CurrentRevision || count != lastRevision.Int64-firstRevision.Int64+1 {
		return HistoryPage{}, Internal("Project history range is inconsistent")
	}
	page.AvailableFromRevision = firstRevision.Int64
	page.AvailableFromOccurredAt = firstOccurredAt.String
	page.CurrentRevisionOccurredAt = lastOccurredAt.String
	return page, nil
}

func (queries *HistoryQueries) normalizeRevision(
	ctx context.Context,
	conn DBTX,
	change BlackboardChange,
) (HistoryRevision, error) {
	if err := validateHistoryEntity(change); err != nil {
		return HistoryRevision{}, err
	}
	revision := HistoryRevision{
		ProjectID: change.ProjectID, Revision: change.Revision, Generation: change.Generation,
		Kind: change.Kind, Entity: HistoryEntity{Type: change.EntityType, ID: change.EntityID},
		OccurredAt: change.CreatedAt,
	}
	switch change.PayloadSchema {
	case LegacyHistorySchema:
		source, evidence, err := normalizeLegacyEvidence(change.Data)
		if err != nil {
			return HistoryRevision{}, err
		}
		revision.Source = source
		revision.Evidence = evidence
		return revision, nil
	case DeltaHistorySchema:
		return queries.normalizeDeltaRevision(ctx, conn, change, revision)
	default:
		return HistoryRevision{}, newHistoryEvidenceConflict("history payload schema %q is unsupported at %s revision %d",
			change.PayloadSchema, change.ProjectID, change.Revision)
	}
}

func normalizeLegacyEvidence(data []byte) (ChangeSource, HistoryMigrationEvidence, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil || envelope == nil {
		return ChangeSource{}, HistoryMigrationEvidence{}, fmt.Errorf("legacy history payload is invalid")
	}
	var payload legacyHistoryPayload
	rawVersion, found := envelope["schema_version"]
	if !found {
		if err := json.Unmarshal(data, &payload); err != nil {
			return ChangeSource{}, HistoryMigrationEvidence{}, fmt.Errorf("legacy history payload is invalid")
		}
		return ChangeSource{Kind: "migration", Name: "Legacy history"}, legacyMigrationEvidence(0, payload), nil
	}
	if bytes.Equal(bytes.TrimSpace(rawVersion), []byte("null")) {
		return ChangeSource{}, HistoryMigrationEvidence{}, fmt.Errorf("legacy history payload schema is invalid")
	}
	var version int
	if err := json.Unmarshal(rawVersion, &version); err != nil {
		return ChangeSource{}, HistoryMigrationEvidence{}, fmt.Errorf("legacy history payload schema is invalid")
	}
	if version < 0 || version > 1 {
		return ChangeSource{}, HistoryMigrationEvidence{}, newHistoryEvidenceConflict("legacy history payload schema %d is unsupported", version)
	}
	source := ChangeSource{Kind: "migration", Name: "Legacy history"}
	if version == 1 {
		if err := decodeHistoryJSON(data, &payload); err != nil {
			return ChangeSource{}, HistoryMigrationEvidence{}, fmt.Errorf("legacy history payload is invalid: %w", err)
		}
		if _, err := validateHistorySource(payload.Source); err != nil {
			return ChangeSource{}, HistoryMigrationEvidence{}, fmt.Errorf("legacy history source is invalid: %w", err)
		}
		if payload.Source.Kind == "migration" {
			source = payload.Source
		}
	} else if err := json.Unmarshal(data, &payload); err != nil {
		return ChangeSource{}, HistoryMigrationEvidence{}, fmt.Errorf("legacy history payload is invalid")
	}
	return source, legacyMigrationEvidence(version, payload), nil
}

func legacyMigrationEvidence(version int, payload legacyHistoryPayload) HistoryMigrationEvidence {
	snapshot := decodeLegacyHistorySnapshot(payload.After)
	if snapshot != nil {
		normalized := normalizeProjectDetail(*snapshot)
		snapshot = &normalized
	}
	return HistoryMigrationEvidence{SchemaVersion: version, snapshot: snapshot}
}

func decodeLegacyHistorySnapshot(raw json.RawMessage) *ProjectDetail {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var detail ProjectDetail
	if err := json.Unmarshal(raw, &detail); err != nil || detail.Project.ID == "" {
		return nil
	}
	return &detail
}

func validateHistoryEntity(change BlackboardChange) error {
	wantType := ""
	requireID := true
	switch change.Kind {
	case ChangeProjectBaseline, ChangeProjectCreated, ChangeProjectRenamed,
		ChangeProjectPaused, ChangeProjectResumed, ChangeProjectCompleted,
		ChangeProjectReopened, ChangeBootstrapCompleted:
		wantType = "project"
	case ChangeHintCreated, ChangeHintWithdrawn:
		wantType = "hint"
	case ChangeIntentCreated, ChangeIntentConcluded, ChangeIntentCancelled:
		wantType = "intent"
	case ChangeFactRetracted, ChangeFactSuperseded:
		wantType = "fact"
	case ChangeIntentsProposed:
		wantType = "intent_batch"
		requireID = false
	default:
		return newHistoryEvidenceConflict("history change kind %q is unsupported", change.Kind)
	}
	if change.EntityType != wantType || (requireID && (change.EntityID == nil || *change.EntityID == "")) ||
		(!requireID && change.EntityID != nil) {
		return fmt.Errorf("history change entity is inconsistent at revision %d", change.Revision)
	}
	if wantType == "project" && (change.EntityID == nil || *change.EntityID != change.ProjectID) {
		return fmt.Errorf("history project entity is inconsistent at revision %d", change.Revision)
	}
	return nil
}

func (queries *HistoryQueries) normalizeDeltaRevision(
	ctx context.Context,
	conn DBTX,
	change BlackboardChange,
	revision HistoryRevision,
) (HistoryRevision, error) {
	result, err := queries.validateDeltaRevision(ctx, conn, change)
	if err != nil {
		return HistoryRevision{}, err
	}
	before, after, err := reconstructDeltaBoundary(ctx, conn, change)
	if err != nil {
		return HistoryRevision{}, err
	}
	revision.Source = change.Source
	revision.Evidence = HistoryTransitionEvidence{Before: before, After: *after, Result: result}
	return revision, nil
}

func (queries *HistoryQueries) validateRevisionForSnapshot(ctx context.Context, conn DBTX, change BlackboardChange) error {
	if err := validateHistoryEntity(change); err != nil {
		return err
	}
	switch change.PayloadSchema {
	case LegacyHistorySchema:
		_, _, err := normalizeLegacyEvidence(change.Data)
		return err
	case DeltaHistorySchema:
		_, err := queries.validateDeltaRevision(ctx, conn, change)
		return err
	default:
		return newHistoryEvidenceConflict("history payload schema %q is unsupported at %s revision %d",
			change.PayloadSchema, change.ProjectID, change.Revision)
	}
}

func (queries *HistoryQueries) validateDeltaRevision(
	ctx context.Context,
	conn DBTX,
	change BlackboardChange,
) (HistoryResultEvidence, error) {
	delta, err := decodeHistoryDelta(change.Delta)
	if err != nil {
		return nil, fmt.Errorf("history delta at revision %d is invalid: %w", change.Revision, err)
	}
	if err := validateHistoryEntity(change); err != nil {
		return nil, err
	}
	if err := validateRetainedHistoryEnvelope(change); err != nil {
		return nil, err
	}
	if delta.Kind != change.Kind {
		return nil, newHistoryEvidenceConflict("history delta kind mismatch at revision %d", change.Revision)
	}
	if err := validateRetainedDeltaEvidence(ctx, conn, change); err != nil {
		return nil, err
	}
	if calculateChangeHash(change) != change.ChangeHash {
		return nil, newHistoryEvidenceConflict("history chain hash mismatch at revision %d", change.Revision)
	}
	result, err := typedHistoryResult(delta)
	if err != nil {
		return nil, fmt.Errorf("history result at revision %d is invalid: %w", change.Revision, err)
	}
	if _, err := validateHistorySource(change.Source); err != nil {
		return nil, err
	}
	return result, nil
}

func validateRetainedHistoryEnvelope(change BlackboardChange) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(change.Data, &fields); err != nil || fields == nil {
		return fmt.Errorf("retained history evidence at revision %d is invalid", change.Revision)
	}
	rawSchema, found := fields["schema_version"]
	if !found || bytes.Equal(bytes.TrimSpace(rawSchema), []byte("null")) {
		return fmt.Errorf("retained history evidence schema at revision %d is invalid", change.Revision)
	}
	var schema int
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		return fmt.Errorf("retained history evidence schema at revision %d is invalid", change.Revision)
	}
	if schema > ChangeSchemaVersion {
		return newHistoryEvidenceConflict("retained history evidence schema %d is unsupported at revision %d", schema, change.Revision)
	}
	if schema != ChangeSchemaVersion {
		return fmt.Errorf("retained history evidence schema %d is invalid at revision %d", schema, change.Revision)
	}
	var envelope storedHistoryEnvelope
	if err := json.Unmarshal(change.Data, &envelope); err != nil {
		return fmt.Errorf("retained history evidence at revision %d is invalid: %w", change.Revision, err)
	}
	if _, err := validateHistorySource(envelope.Source); err != nil {
		return fmt.Errorf("retained history evidence at revision %d is invalid: %w", change.Revision, err)
	}
	return nil
}

func validateRetainedDeltaEvidence(ctx context.Context, conn DBTX, change BlackboardChange) error {
	if !storedHistoryDataMatchesDelta(change) {
		// Rebuild the compatibility envelope through the Project-owned reducer.
		// The result is intentionally discarded; this check only proves the
		// retained evidence still agrees with the authoritative typed delta.
		if _, err := compatibilityHistoryData(ctx, conn, change); err != nil {
			return fmt.Errorf("retained history evidence at revision %d is invalid: %w", change.Revision, err)
		}
		return fmt.Errorf("retained history evidence at revision %d is invalid", change.Revision)
	}
	return nil
}

func typedHistoryResult(delta HistoryDelta) (HistoryResultEvidence, error) {
	switch delta.Kind {
	case ChangeProjectCreated, ChangeProjectRenamed, ChangeProjectPaused, ChangeProjectResumed, ChangeIntentCreated:
		if len(delta.Result) != 0 {
			return nil, fmt.Errorf("%s cannot contain result evidence", delta.Kind)
		}
		return HistoryNoResult{}, nil
	case ChangeHintCreated:
		var result struct {
			AffectedRunCount *int `json:"affected_run_count"`
		}
		if err := decodeHistoryResult(delta.Result, &result); err != nil || result.AffectedRunCount == nil || *result.AffectedRunCount < 0 {
			return nil, fmt.Errorf("affected Run count is missing")
		}
		return HistoryAffectedRunsResult{AffectedRunCount: *result.AffectedRunCount}, nil
	case ChangeHintWithdrawn, ChangeIntentCancelled, ChangeFactRetracted:
		var result struct {
			Reason           string `json:"reason"`
			AffectedRunCount *int   `json:"affected_run_count"`
		}
		if err := decodeHistoryResult(delta.Result, &result); err != nil || result.Reason == "" || result.AffectedRunCount == nil || *result.AffectedRunCount < 0 {
			return nil, fmt.Errorf("reason or affected Run count is missing")
		}
		return HistoryReasonResult{Reason: result.Reason, AffectedRunCount: *result.AffectedRunCount}, nil
	case ChangeFactSuperseded:
		var result struct {
			Reason            string `json:"reason"`
			ReplacementFactID string `json:"replacement_fact_id"`
			AffectedRunCount  *int   `json:"affected_run_count"`
		}
		if err := decodeHistoryResult(delta.Result, &result); err != nil || result.Reason == "" ||
			result.ReplacementFactID == "" || result.AffectedRunCount == nil || *result.AffectedRunCount < 0 {
			return nil, fmt.Errorf("Fact supersession result is incomplete")
		}
		return HistorySupersedeResult{
			Reason: result.Reason, ReplacementFactID: result.ReplacementFactID,
			AffectedRunCount: *result.AffectedRunCount,
		}, nil
	case ChangeIntentsProposed:
		var result struct {
			IntentIDs []string `json:"intent_ids"`
		}
		if err := decodeHistoryResult(delta.Result, &result); err != nil || len(result.IntentIDs) == 0 {
			return nil, fmt.Errorf("proposed Intent identities are missing")
		}
		return HistoryProposedIntentsResult{IntentIDs: append([]string(nil), result.IntentIDs...)}, nil
	case ChangeIntentConcluded:
		var result struct {
			FactID string `json:"fact_id"`
		}
		if err := decodeHistoryResult(delta.Result, &result); err != nil || result.FactID == "" {
			return nil, fmt.Errorf("produced Fact identity is missing")
		}
		return HistoryConcludedIntentResult{FactID: result.FactID}, nil
	case ChangeProjectCompleted:
		var result struct {
			CompletionIntentID string `json:"completion_intent_id"`
			AffectedRunCount   *int   `json:"affected_run_count,omitempty"`
		}
		if err := decodeHistoryResult(delta.Result, &result); err != nil || result.CompletionIntentID == "" ||
			(result.AffectedRunCount != nil && *result.AffectedRunCount < 0) {
			return nil, fmt.Errorf("completion Intent identity is missing")
		}
		return HistoryCompletedProjectResult{
			CompletionIntentID: result.CompletionIntentID, AffectedRunCount: result.AffectedRunCount,
		}, nil
	case ChangeProjectReopened:
		var result struct {
			HintID             string `json:"hint_id"`
			PreviousGeneration int    `json:"previous_generation"`
		}
		if err := decodeHistoryResult(delta.Result, &result); err != nil || result.HintID == "" || result.PreviousGeneration < 1 {
			return nil, fmt.Errorf("Project reopen result is incomplete")
		}
		return HistoryReopenedProjectResult{HintID: result.HintID, PreviousGeneration: result.PreviousGeneration}, nil
	case ChangeBootstrapCompleted:
		var result struct {
			FactID             string `json:"fact_id"`
			TargetIntentID     string `json:"target_intent_id"`
			CompletionIntentID string `json:"completion_intent_id"`
		}
		if err := decodeHistoryResult(delta.Result, &result); err != nil || result.FactID == "" ||
			result.TargetIntentID == "" || result.CompletionIntentID == "" {
			return nil, fmt.Errorf("Bootstrap completion result is incomplete")
		}
		return HistoryBootstrapCompletedResult{
			FactID: result.FactID, TargetIntentID: result.TargetIntentID,
			CompletionIntentID: result.CompletionIntentID,
		}, nil
	default:
		return nil, newHistoryEvidenceConflict("history change kind %q is unsupported", delta.Kind)
	}
}

func decodeHistoryResult(result map[string]any, destination any) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return decodeHistoryJSON(data, destination)
}

func decodeHistoryJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("history JSON contains trailing data")
	}
	return nil
}

func validateHistorySource(source ChangeSource) (ChangeSource, error) {
	if source.Name == "" {
		return ChangeSource{}, fmt.Errorf("history source name is missing")
	}
	switch source.Kind {
	case "operator":
		if source.RunID != nil {
			return ChangeSource{}, fmt.Errorf("operator history source contains a Run ID")
		}
	case "run":
		if source.RunID == nil || *source.RunID == "" {
			return ChangeSource{}, fmt.Errorf("Run history source is missing its Run ID")
		}
	case "migration":
		if source.RunID != nil {
			return ChangeSource{}, fmt.Errorf("migration history source contains a Run ID")
		}
	default:
		return ChangeSource{}, fmt.Errorf("history source kind is invalid")
	}
	return source, nil
}

func historyQueryError(err error) error {
	var domainErr *DomainError
	if errors.As(err, &domainErr) {
		return err
	}
	var unsupported *unsupportedHistorySchemaError
	var conflict *historyEvidenceConflictError
	if errors.As(err, &unsupported) || errors.As(err, &conflict) {
		return Conflict(err.Error())
	}
	return Internal(err.Error())
}
