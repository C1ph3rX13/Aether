package query

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/database"
	"github.com/C1ph3rX13/Aether/internal/project"
)

// logTestExecution is the complete execution participant used by the Project Read Module
// integration fixtures. Query tests currently use successful no-op behavior, but
// every Project command still passes through an explicit, observable contract.
type logTestExecution struct {
	baselineCalls     []logExecutionBaselineCall
	previewCalls      []logExecutionPreviewCall
	cancellationCalls []logExecutionCancellationCall
	cleanupCalls      []logExecutionCleanupCall

	previewCount    int
	cancelCount     int
	cleanupJobID    string
	baselineErr     error
	previewErr      error
	cancellationErr error
	cleanupErr      error
}

type logExecutionBaselineCall struct {
	projectID  string
	generation int
	revision   int64
}

type logExecutionPreviewCall struct {
	projectID string
	selector  project.RunCancellationSelector
}

type logExecutionCancellationCall struct {
	projectID string
	revision  int64
	reason    project.CancellationReason
	selector  project.RunCancellationSelector
}

type logExecutionCleanupCall struct {
	projectID string
}

var _ project.ExecutionPort = (*logTestExecution)(nil)

func (execution *logTestExecution) EnsureReasonSchedulingBaseline(
	_ context.Context, _ project.DBTX, projectID string, generation int, revision int64, _ string,
) error {
	execution.baselineCalls = append(execution.baselineCalls, logExecutionBaselineCall{
		projectID: projectID, generation: generation, revision: revision,
	})
	return execution.baselineErr
}

func applyLogTestRunResult(
	t *testing.T,
	projects *project.Service,
	db *database.DB,
	command project.ApplyRunResultCommand,
) project.ApplyRunResult {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := projects.ApplyRunResult(t.Context(), tx, command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stale || result.Change == nil {
		t.Fatalf("Run result was not committed: %+v", result)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result
}

func (execution *logTestExecution) PreviewRunCancellation(
	_ context.Context, _ project.DBTX, projectID string, selector project.RunCancellationSelector,
) (int, error) {
	execution.previewCalls = append(execution.previewCalls, logExecutionPreviewCall{
		projectID: projectID, selector: selector,
	})
	if execution.previewErr != nil {
		return 0, execution.previewErr
	}
	return execution.previewCount, nil
}

func (execution *logTestExecution) CancelRuns(
	_ context.Context, _ project.DBTX, projectID string, revision int64,
	reason project.CancellationReason, selector project.RunCancellationSelector,
) (int, error) {
	execution.cancellationCalls = append(execution.cancellationCalls, logExecutionCancellationCall{
		projectID: projectID, revision: revision, reason: reason, selector: selector,
	})
	if execution.cancellationErr != nil {
		return 0, execution.cancellationErr
	}
	return execution.cancelCount, nil
}

func (execution *logTestExecution) QueueProjectCleanup(
	_ context.Context, _ project.DBTX, projectID, _ string,
) (string, error) {
	execution.cleanupCalls = append(execution.cleanupCalls, logExecutionCleanupCall{projectID: projectID})
	if execution.cleanupErr != nil {
		return "", execution.cleanupErr
	}
	if execution.cleanupJobID == "" {
		return "test-cleanup", nil
	}
	return execution.cleanupJobID, nil
}

func newLogTestServices(t *testing.T) (*Service, *project.Service, *database.DB) {
	t.Helper()
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "aether.db")); err != nil {
		t.Fatal(err)
	}
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		INSERT INTO blackboard_history_retention_operations (operation_id, active, created_at)
		VALUES ('test-history-mutation', 1, ?)`, project.UTCNow()); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	execution := &logTestExecution{}
	projects, err := project.NewService(db, nil, execution, project.ServiceOptions{OperatorName: "test-operator"})
	if err != nil {
		t.Fatal(err)
	}
	return newQueryService(db), projects, db
}

func TestListProjectLogProjectsCreatedRevisionAsStableCompositeGroup(t *testing.T) {
	queries, projects, _ := newLogTestServices(t)
	created, err := projects.Create(t.Context(), project.CreateProjectRequest{
		Title:            "Composite creation",
		Origin:           "starting evidence",
		Goal:             "desired outcome",
		BootstrapEnabled: true,
		Hints: []project.CreateHintInline{
			{Content: "second hint"},
			{Content: "first hint"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	page, err := queries.ListProjectLog(t.Context(), created.Project.ID, ProjectLogQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.ProjectID != created.Project.ID || page.AvailableFromRevision != 1 || page.CurrentRevision != 1 {
		t.Fatalf("Log range: %+v", page)
	}
	if page.AvailableFromOccurredAt == "" || page.CurrentRevisionOccurredAt != page.AvailableFromOccurredAt {
		t.Fatalf("Log time range: %+v", page)
	}
	if page.NextBeforeRevision != nil || len(page.Revisions) != 1 {
		t.Fatalf("Log page: %+v", page)
	}
	group := page.Revisions[0]
	if group.Revision != 1 || group.Generation != 1 || group.ChangeKind != project.ChangeProjectCreated || group.OccurredAt == "" {
		t.Fatalf("creation group: %+v", group)
	}
	if group.Source.Kind != LogSourceOperator || group.Source.Name != "test-operator" || group.Source.RunID != nil {
		t.Fatalf("creation source: %+v", group.Source)
	}
	wantKinds := []LogEventKind{
		LogEventProjectCreated,
		LogEventHintCreated,
		LogEventHintCreated,
		LogEventIntentCreated,
	}
	if len(group.Events) != len(wantKinds) {
		t.Fatalf("creation events=%d, want %d: %+v", len(group.Events), len(wantKinds), group.Events)
	}
	seenIDs := map[string]struct{}{}
	for index, event := range group.Events {
		base := event.logEvent()
		if base.Kind != wantKinds[index] || base.EventID == "" {
			t.Fatalf("creation event %d: %+v", index, base)
		}
		if _, duplicate := seenIDs[base.EventID]; duplicate {
			t.Fatalf("duplicate event ID %q", base.EventID)
		}
		seenIDs[base.EventID] = struct{}{}
		if !strings.HasPrefix(base.EventID, created.Project.ID+":r1:") {
			t.Fatalf("event ID %q is not scoped to Project and revision", base.EventID)
		}
	}
	createdEvent, ok := group.Events[0].(ProjectCreatedEvent)
	if !ok || createdEvent.Origin.ID != "origin" || createdEvent.Goal.ID != "goal" ||
		createdEvent.FocusTarget == nil || createdEvent.FocusTarget.Type != LogFocusFact || createdEvent.FocusTarget.ID != "origin" {
		t.Fatalf("project_created event: %#v", group.Events[0])
	}
	bootstrapEvent, ok := group.Events[3].(IntentCreatedEvent)
	if !ok || bootstrapEvent.IntentID == "" || bootstrapEvent.IntentKind != project.IntentKindBootstrap ||
		bootstrapEvent.FocusTarget == nil || bootstrapEvent.FocusTarget.Type != LogFocusIntent || bootstrapEvent.FocusTarget.ID != bootstrapEvent.IntentID {
		t.Fatalf("bootstrap intent event: %#v", group.Events[3])
	}

	repeated, err := queries.ListProjectLog(t.Context(), created.Project.ID, ProjectLogQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(page, repeated) {
		t.Fatalf("Log event identities changed between reads:\nfirst=%+v\nsecond=%+v", page, repeated)
	}
}

func TestListProjectLogProjectsOperatorAndRunChangesIntoClosedEventUnion(t *testing.T) {
	queries, projects, db := newLogTestServices(t)
	created, err := projects.Create(t.Context(), project.CreateProjectRequest{
		Title: "Complete history", Origin: "initial origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	projectID := created.Project.ID
	if _, err := projects.UpdateTitle(t.Context(), projectID, project.UpdateProjectTitleRequest{
		Title: "Renamed history", ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.PauseProject(t.Context(), projectID, project.ProjectLifecycleRequest{ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.ResumeProject(t.Context(), projectID, project.ProjectLifecycleRequest{ExpectedRevision: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.CreateHint(t.Context(), projectID, project.CreateHintRequest{
		Content: "temporary guidance", ExpectedRevision: 4,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.WithdrawHint(t.Context(), projectID, "h001", project.WithdrawHintRequest{
		Reason: "guidance expired", ExpectedRevision: 5,
	}); err != nil {
		t.Fatal(err)
	}
	superseded, err := projects.SupersedeFact(t.Context(), projectID, "origin", project.SupersedeFactRequest{
		Description: "corrected origin", Reason: "verified correction", ExpectedRevision: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	proposed := applyLogTestRunResult(t, projects, db, project.ApplyRunResultCommand{
		RunID: "run-reason", ProjectID: projectID, Task: project.RunTaskReason,
		Generation: 1, InputRevision: 7, Worker: "reason-worker", AppliedAt: project.UTCNow(),
		Outcome: project.RunOutcome{Kind: project.RunOutcomeProposeIntents, Intents: []project.ProposedRunIntent{
			{Description: "first exploration", From: []string{superseded.Replacement.ID}},
			{Description: "second exploration", From: []string{superseded.Replacement.ID}},
		}},
	})
	firstIntent, secondIntent := proposed.CreatedIntentIDs[0], proposed.CreatedIntentIDs[1]
	concluded := applyLogTestRunResult(t, projects, db, project.ApplyRunResultCommand{
		RunID: "run-explore", ProjectID: projectID, TargetIntent: &firstIntent, Task: project.RunTaskExplore,
		Generation: 1, InputRevision: 8, Worker: "explore-worker", AppliedAt: project.UTCNow(),
		Outcome: project.RunOutcome{Kind: project.RunOutcomeConcludeIntent, FactDescription: "exploration evidence"},
	})
	if _, err := projects.CancelIntent(t.Context(), projectID, secondIntent, project.CancelIntentRequest{
		Reason: "direction closed", ExpectedRevision: 9,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.RetractFact(t.Context(), projectID, concluded.CreatedFactIDs[0], project.RetractFactRequest{
		Reason: "evidence invalid", ExpectedRevision: 10,
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := projects.CompleteProject(t.Context(), projectID, project.CompleteRequest{
		From: []string{superseded.Replacement.ID}, Description: "goal reached", ExpectedRevision: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.ReopenProject(t.Context(), projectID, project.ReopenRequest{
		Feedback: "new generation feedback", ExpectedRevision: completed.ProjectRevision,
	}); err != nil {
		t.Fatal(err)
	}

	page, err := queries.ListProjectLog(t.Context(), projectID, ProjectLogQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Revisions) != 13 || page.CurrentRevision != 13 || page.NextBeforeRevision != nil {
		t.Fatalf("complete Log page: %+v", page)
	}
	wantByRevision := map[int64][]LogEventKind{
		1:  {LogEventProjectCreated},
		2:  {LogEventProjectRenamed},
		3:  {LogEventProjectPaused},
		4:  {LogEventProjectResumed},
		5:  {LogEventHintCreated},
		6:  {LogEventHintWithdrawn},
		7:  {LogEventFactSuperseded},
		8:  {LogEventIntentCreated, LogEventIntentCreated},
		9:  {LogEventIntentConcluded},
		10: {LogEventIntentCancelled},
		11: {LogEventFactRetracted},
		12: {LogEventIntentCreated, LogEventProjectCompleted},
		13: {LogEventProjectReopened, LogEventHintCreated},
	}
	seenKinds := map[LogEventKind]struct{}{}
	for index, group := range page.Revisions {
		wantRevision := int64(13 - index)
		if group.Revision != wantRevision {
			t.Fatalf("group %d revision=%d, want %d", index, group.Revision, wantRevision)
		}
		wantKinds := wantByRevision[group.Revision]
		if len(group.Events) != len(wantKinds) {
			t.Fatalf("R%d events=%#v, want kinds=%v", group.Revision, group.Events, wantKinds)
		}
		for eventIndex, event := range group.Events {
			kind := event.logEvent().Kind
			if kind != wantKinds[eventIndex] {
				t.Fatalf("R%d event %d kind=%s, want %s", group.Revision, eventIndex, kind, wantKinds[eventIndex])
			}
			seenKinds[kind] = struct{}{}
		}
	}
	allNonMigrationKinds := []LogEventKind{
		LogEventProjectCreated, LogEventProjectRenamed,
		LogEventProjectPaused, LogEventProjectResumed,
		LogEventProjectReopened, LogEventProjectCompleted,
		LogEventHintCreated, LogEventHintWithdrawn,
		LogEventIntentCreated, LogEventIntentConcluded,
		LogEventIntentCancelled, LogEventFactRetracted,
		LogEventFactSuperseded,
	}
	for _, kind := range allNonMigrationKinds {
		if _, found := seenKinds[kind]; !found {
			t.Errorf("event kind %s was not projected", kind)
		}
	}

	conclusionGroup := page.Revisions[13-9]
	if conclusionGroup.Source.Kind != LogSourceRun || conclusionGroup.Source.Name != "explore-worker" ||
		conclusionGroup.Source.RunID == nil || *conclusionGroup.Source.RunID != "run-explore" {
		t.Fatalf("Run source: %+v", conclusionGroup.Source)
	}
	conclusionEvent, ok := conclusionGroup.Events[0].(IntentConcludedEvent)
	if !ok || conclusionEvent.ProducedFact.ID != concluded.CreatedFactIDs[0] ||
		conclusionEvent.FocusTarget == nil || conclusionEvent.FocusTarget.ID != conclusionEvent.ProducedFact.ID {
		t.Fatalf("intent_concluded event: %#v", conclusionGroup.Events[0])
	}
	supersededGroup := page.Revisions[13-7]
	supersededEvent, ok := supersededGroup.Events[0].(FactSupersededEvent)
	if !ok || supersededEvent.OriginalFact.ID != "origin" || supersededEvent.ReplacementFact.ID != superseded.Replacement.ID ||
		supersededEvent.Actor != "test-operator" || supersededEvent.Reason != "verified correction" {
		t.Fatalf("fact_superseded event: %#v", supersededGroup.Events[0])
	}
	completionGroup := page.Revisions[13-12]
	completionEvent, ok := completionGroup.Events[1].(ProjectCompletedEvent)
	if !ok || completionEvent.CompletionIntentID != completed.Intent.ID || completionEvent.Description != "goal reached" ||
		!reflect.DeepEqual(completionEvent.SourceFactIDs, []string{superseded.Replacement.ID}) || completionEvent.CompletionGeneration != 1 {
		t.Fatalf("project_completed event: %#v", completionGroup.Events[1])
	}
	wire, err := json.Marshal(struct {
		Superseded FactSupersededEvent   `json:"superseded"`
		Completed  ProjectCompletedEvent `json:"completed"`
	}{Superseded: supersededEvent, Completed: completionEvent})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["superseded"]["original_fact"] == nil || fields["superseded"]["replacement_fact"] == nil ||
		fields["superseded"]["original"] != nil || fields["superseded"]["replacement"] != nil {
		t.Fatalf("fact_superseded wire fields: %s", wire)
	}
	if fields["completed"]["completion_generation"] == nil || fields["completed"]["generation"] != nil {
		t.Fatalf("project_completed wire fields: %s", wire)
	}
	reopenedGroup := page.Revisions[0]
	reopenedEvent, ok := reopenedGroup.Events[0].(ProjectReopenedEvent)
	if !ok || reopenedEvent.PreviousGeneration != 1 || reopenedEvent.CurrentGeneration != 2 || reopenedEvent.FeedbackHintID != "h002" {
		t.Fatalf("project_reopened event: %#v", reopenedGroup.Events[0])
	}

	projectedKinds := make([]string, 0, len(seenKinds))
	for kind := range seenKinds {
		projectedKinds = append(projectedKinds, string(kind))
	}
	sort.Strings(projectedKinds)
	if len(projectedKinds) != len(allNonMigrationKinds) {
		t.Fatalf("projected event taxonomy=%v", projectedKinds)
	}
}

func TestListProjectLogPaginatesAtomicBootstrapCompletionGroups(t *testing.T) {
	queries, projects, db := newLogTestServices(t)
	created, err := projects.Create(t.Context(), project.CreateProjectRequest{
		Title: "Bootstrap history", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Intents) != 1 {
		t.Fatalf("Bootstrap Project intents: %+v", created.Intents)
	}
	targetIntentID := created.Intents[0].ID
	completed := applyLogTestRunResult(t, projects, db, project.ApplyRunResultCommand{
		RunID: "run-bootstrap", ProjectID: created.Project.ID, TargetIntent: &targetIntentID,
		Task: project.RunTaskBootstrap, Generation: 1, InputRevision: 1,
		Worker: "bootstrap-worker", AppliedAt: project.UTCNow(),
		Outcome: project.RunOutcome{
			Kind: project.RunOutcomeCompleteProject, Description: "bootstrap reached the goal",
			FactDescription: "bootstrap evidence",
		},
	})
	if completed.ProjectRevision != 2 {
		t.Fatalf("Bootstrap completion: %+v", completed)
	}

	latest, err := queries.ListProjectLog(t.Context(), created.Project.ID, ProjectLogQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if latest.AvailableFromRevision != 1 || latest.CurrentRevision != 2 || len(latest.Revisions) != 1 ||
		latest.Revisions[0].Revision != 2 || latest.NextBeforeRevision == nil || *latest.NextBeforeRevision != 2 {
		t.Fatalf("latest Log page: %+v", latest)
	}
	group := latest.Revisions[0]
	wantKinds := []LogEventKind{
		LogEventIntentConcluded,
		LogEventIntentCreated,
		LogEventProjectCompleted,
	}
	if len(group.Events) != len(wantKinds) {
		t.Fatalf("Bootstrap completion events: %#v", group.Events)
	}
	for index, event := range group.Events {
		if event.logEvent().Kind != wantKinds[index] {
			t.Fatalf("Bootstrap event %d kind=%s, want %s", index, event.logEvent().Kind, wantKinds[index])
		}
	}
	if group.Source.Kind != LogSourceRun || group.Source.RunID == nil || *group.Source.RunID != "run-bootstrap" {
		t.Fatalf("Bootstrap source: %+v", group.Source)
	}

	older, err := queries.ListProjectLog(t.Context(), created.Project.ID, ProjectLogQuery{BeforeRevision: *latest.NextBeforeRevision, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if older.AvailableFromRevision != latest.AvailableFromRevision ||
		older.AvailableFromOccurredAt != latest.AvailableFromOccurredAt ||
		older.CurrentRevision != latest.CurrentRevision ||
		older.CurrentRevisionOccurredAt != latest.CurrentRevisionOccurredAt ||
		len(older.Revisions) != 1 || older.Revisions[0].Revision != 1 || older.NextBeforeRevision != nil {
		t.Fatalf("older Log page: %+v", older)
	}

	empty, err := queries.ListProjectLog(t.Context(), created.Project.ID, ProjectLogQuery{BeforeRevision: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Revisions) != 0 || empty.NextBeforeRevision != nil || empty.CurrentRevision != 2 {
		t.Fatalf("exclusive empty Log page: %+v", empty)
	}
}

func TestListProjectLogProjectsRecognizedBaselinesAndLegacyHistoryAsMigration(t *testing.T) {
	tests := []struct {
		name           string
		kind           project.ChangeKind
		data           string
		wantSchema     int
		wantSourceName string
	}{
		{
			name: "current baseline",
			kind: project.ChangeProjectBaseline,
			data: `{"schema_version":1,"source":{"kind":"migration","name":"0013_project_history_baseline"},` +
				`"before":null,"after":{"retained":"migration snapshot"}}`,
			wantSchema:     project.ChangeSchemaVersion,
			wantSourceName: "0013_project_history_baseline",
		},
		{
			name:           "legacy payload without version",
			kind:           project.ChangeProjectCreated,
			data:           `{"legacy":"retained notification"}`,
			wantSchema:     0,
			wantSourceName: "Legacy history",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queries, projects, db := newLogTestServices(t)
			created, err := projects.Create(t.Context(), project.CreateProjectRequest{
				Title: "Migrated history", Origin: "origin", Goal: "goal",
			})
			if err != nil {
				t.Fatal(err)
			}
			conn, err := db.GetConn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(t.Context(), `
				UPDATE blackboard_changes SET kind = ?, data = ?, payload_schema = ?
				WHERE project_id = ? AND revision = 1`,
				test.kind, test.data, project.LegacyHistorySchema, created.Project.ID,
			); err != nil {
				conn.Close()
				t.Fatal(err)
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}

			page, err := queries.ListProjectLog(t.Context(), created.Project.ID, ProjectLogQuery{})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Revisions) != 1 {
				t.Fatalf("migration revisions: %+v", page.Revisions)
			}
			group := page.Revisions[0]
			if group.Source.Kind != LogSourceMigration || group.Source.Name != test.wantSourceName || group.Source.RunID != nil {
				t.Fatalf("migration source: %+v", group.Source)
			}
			if len(group.Events) != 1 {
				t.Fatalf("migration events: %#v", group.Events)
			}
			event, ok := group.Events[0].(MigrationEvent)
			if !ok || event.Kind != LogEventMigration || event.OriginalChangeKind != test.kind ||
				event.SchemaVersion != test.wantSchema || event.EntityType != "project" ||
				event.EntityID == nil || *event.EntityID != created.Project.ID || event.FocusTarget != nil {
				t.Fatalf("migration event: %#v", group.Events[0])
			}
		})
	}
}

func TestListProjectLogFailsClosedWhenRequiredAfterImageEvidenceIsMissingOrEmpty(t *testing.T) {
	tests := []struct {
		name     string
		request  project.CreateProjectRequest
		mutation string
	}{
		{
			name: "project title is missing",
			request: project.CreateProjectRequest{
				Title: "Strict evidence", Origin: "origin evidence", Goal: "goal evidence",
			},
			mutation: "json_remove(data, '$.after.project.title')",
		},
		{
			name: "fact description is empty",
			request: project.CreateProjectRequest{
				Title: "Strict evidence", Origin: "origin evidence", Goal: "goal evidence",
			},
			mutation: "json_set(data, '$.after.facts[0].description', '')",
		},
		{
			name: "hint content is missing",
			request: project.CreateProjectRequest{
				Title: "Strict evidence", Origin: "origin evidence", Goal: "goal evidence",
				Hints: []project.CreateHintInline{{Content: "important guidance"}},
			},
			mutation: "json_remove(data, '$.after.hints[0].content')",
		},
		{
			name: "intent status is missing",
			request: project.CreateProjectRequest{
				Title: "Strict evidence", Origin: "origin evidence", Goal: "goal evidence", BootstrapEnabled: true,
			},
			mutation: "json_remove(data, '$.after.intents[0].status')",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queries, projects, db := newLogTestServices(t)
			created, err := projects.Create(t.Context(), test.request)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := db.GetConn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(t.Context(), `
				UPDATE blackboard_changes SET data = `+test.mutation+`
				WHERE project_id = ? AND revision = 1`, created.Project.ID,
			); err != nil {
				conn.Close()
				t.Fatal(err)
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}

			page, err := queries.ListProjectLog(t.Context(), created.Project.ID, ProjectLogQuery{})
			assertLogErrorCode(t, err, project.ErrorInternal)
			if len(page.Revisions) != 0 {
				t.Fatalf("failed projection returned partial revisions: %+v", page.Revisions)
			}
		})
	}
}

func TestListProjectLogFailsClosedWhenWithdrawnHintContentIsEmpty(t *testing.T) {
	queries, projects, db := newLogTestServices(t)
	created, err := projects.Create(t.Context(), project.CreateProjectRequest{
		Title: "Strict hint evidence", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.CreateHint(t.Context(), created.Project.ID, project.CreateHintRequest{
		Content: "temporary guidance", ExpectedRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.WithdrawHint(t.Context(), created.Project.ID, "h001", project.WithdrawHintRequest{
		Reason: "guidance expired", ExpectedRevision: 2,
	}); err != nil {
		t.Fatal(err)
	}
	mutateLogHistory(t, db, created.Project.ID, 3,
		"json_set(data, '$.after.hints[0].content', '')")

	page, err := queries.ListProjectLog(t.Context(), created.Project.ID, ProjectLogQuery{})
	assertLogErrorCode(t, err, project.ErrorInternal)
	if len(page.Revisions) != 0 {
		t.Fatalf("failed projection returned partial revisions: %+v", page.Revisions)
	}
}

func TestListProjectLogFailsClosedWhenCancelledIntentDescriptionIsMissing(t *testing.T) {
	queries, projects, db := newLogTestServices(t)
	created, err := projects.Create(t.Context(), project.CreateProjectRequest{
		Title: "Strict intent evidence", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposed := applyLogTestRunResult(t, projects, db, project.ApplyRunResultCommand{
		RunID: "run-reason", ProjectID: created.Project.ID, Task: project.RunTaskReason,
		Generation: 1, InputRevision: 1, Worker: "reason-worker", AppliedAt: project.UTCNow(),
		Outcome: project.RunOutcome{Kind: project.RunOutcomeProposeIntents, Intents: []project.ProposedRunIntent{
			{Description: "explore evidence", From: []string{"origin"}},
		}},
	})
	intentID := proposed.CreatedIntentIDs[0]
	if _, err := projects.CancelIntent(t.Context(), created.Project.ID, intentID, project.CancelIntentRequest{
		Reason: "direction closed", ExpectedRevision: 2,
	}); err != nil {
		t.Fatal(err)
	}
	mutateLogHistory(t, db, created.Project.ID, 3,
		"json_remove(data, '$.after.intents[0].cancellation_reason')")

	page, err := queries.ListProjectLog(t.Context(), created.Project.ID, ProjectLogQuery{})
	assertLogErrorCode(t, err, project.ErrorInternal)
	if len(page.Revisions) != 0 {
		t.Fatalf("failed projection returned partial revisions: %+v", page.Revisions)
	}
}

func TestListProjectLogFailsClosedWhenRetractedFactDescriptionIsEmpty(t *testing.T) {
	queries, projects, db := newLogTestServices(t)
	created, err := projects.Create(t.Context(), project.CreateProjectRequest{
		Title: "Strict fact evidence", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposed := applyLogTestRunResult(t, projects, db, project.ApplyRunResultCommand{
		RunID: "run-reason", ProjectID: created.Project.ID, Task: project.RunTaskReason,
		Generation: 1, InputRevision: 1, Worker: "reason-worker", AppliedAt: project.UTCNow(),
		Outcome: project.RunOutcome{Kind: project.RunOutcomeProposeIntents, Intents: []project.ProposedRunIntent{
			{Description: "explore evidence", From: []string{"origin"}},
		}},
	})
	intentID := proposed.CreatedIntentIDs[0]
	concluded := applyLogTestRunResult(t, projects, db, project.ApplyRunResultCommand{
		RunID: "run-explore", ProjectID: created.Project.ID, TargetIntent: &intentID,
		Task: project.RunTaskExplore, Generation: 1, InputRevision: 2,
		Worker: "explore-worker", AppliedAt: project.UTCNow(),
		Outcome: project.RunOutcome{Kind: project.RunOutcomeConcludeIntent, FactDescription: "new evidence"},
	})
	if _, err := projects.RetractFact(t.Context(), created.Project.ID, concluded.CreatedFactIDs[0], project.RetractFactRequest{
		Reason: "evidence invalid", ExpectedRevision: 3,
	}); err != nil {
		t.Fatal(err)
	}
	mutateLogHistory(t, db, created.Project.ID, 4,
		"json_set(data, '$.after.facts[0].description', '')")

	page, err := queries.ListProjectLog(t.Context(), created.Project.ID, ProjectLogQuery{})
	assertLogErrorCode(t, err, project.ErrorInternal)
	if len(page.Revisions) != 0 {
		t.Fatalf("failed projection returned partial revisions: %+v", page.Revisions)
	}
}

func mutateLogHistory(t *testing.T, db *database.DB, projectID string, revision int64, mutation string) {
	t.Helper()
	conn, err := db.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `
		UPDATE blackboard_changes SET data = `+mutation+`
		WHERE project_id = ? AND revision = ?`, projectID, revision,
	); err != nil {
		t.Fatal(err)
	}
}

func assertLogErrorCode(t *testing.T, err error, want project.ErrorCode) {
	t.Helper()
	var domainErr *project.DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != want {
		t.Fatalf("error=%v, want project error code %s", err, want)
	}
}

func TestListProjectLogFailsClosedWithStableDomainErrors(t *testing.T) {
	assertCode := func(t *testing.T, err error, want project.ErrorCode) {
		t.Helper()
		var domainErr *project.DomainError
		if !errors.As(err, &domainErr) || domainErr.Code != want {
			t.Fatalf("error=%v, want project error code %s", err, want)
		}
	}

	t.Run("query validation", func(t *testing.T) {
		queries, _, _ := newLogTestServices(t)
		for _, query := range []ProjectLogQuery{
			{BeforeRevision: -1},
			{Limit: -1},
			{Limit: maximumLogLimit + 1},
		} {
			_, err := queries.ListProjectLog(t.Context(), "proj_missing", query)
			assertCode(t, err, project.ErrorInvalidArgument)
		}
	})

	t.Run("project not found", func(t *testing.T) {
		queries, _, _ := newLogTestServices(t)
		_, err := queries.ListProjectLog(t.Context(), "proj_missing", ProjectLogQuery{})
		assertCode(t, err, project.ErrorNotFound)
	})

	t.Run("internal history gap", func(t *testing.T) {
		queries, projects, db := newLogTestServices(t)
		created, err := projects.Create(t.Context(), project.CreateProjectRequest{
			Title: "Gapped history", Origin: "origin", Goal: "goal",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := projects.UpdateTitle(t.Context(), created.Project.ID, project.UpdateProjectTitleRequest{
			Title: "Revision two", ExpectedRevision: 1,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := projects.PauseProject(t.Context(), created.Project.ID, project.ProjectLifecycleRequest{
			ExpectedRevision: 2,
		}); err != nil {
			t.Fatal(err)
		}
		conn, err := db.GetConn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(t.Context(),
			"DELETE FROM blackboard_changes WHERE project_id = ? AND revision = 2", created.Project.ID,
		); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		page, err := queries.ListProjectLog(t.Context(), created.Project.ID, ProjectLogQuery{})
		assertCode(t, err, project.ErrorInternal)
		if len(page.Revisions) != 0 {
			t.Fatalf("gapped projection returned partial revisions: %+v", page.Revisions)
		}
	})

	tests := []struct {
		name     string
		mutation string
		args     []any
		wantCode project.ErrorCode
	}{
		{
			name:     "future schema",
			mutation: `UPDATE blackboard_changes SET data = json_set(data, '$.schema_version', ?) WHERE project_id = ?`,
			args:     []any{project.ChangeSchemaVersion + 1},
			wantCode: project.ErrorConflict,
		},
		{
			name:     "unknown current change kind",
			mutation: `UPDATE blackboard_changes SET kind = 'future_change' WHERE project_id = ?`,
			wantCode: project.ErrorConflict,
		},
		{
			name:     "malformed payload",
			mutation: `UPDATE blackboard_changes SET data = '{' WHERE project_id = ?`,
			wantCode: project.ErrorInternal,
		},
		{
			name:     "null schema version",
			mutation: `UPDATE blackboard_changes SET data = '{"schema_version":null}' WHERE project_id = ?`,
			wantCode: project.ErrorInternal,
		},
		{
			name:     "unknown current payload field",
			mutation: `UPDATE blackboard_changes SET data = json_set(data, '$.future_field', true) WHERE project_id = ?`,
			wantCode: project.ErrorInternal,
		},
		{
			name:     "missing required entity identity",
			mutation: `UPDATE blackboard_changes SET entity_id = NULL WHERE project_id = ?`,
			wantCode: project.ErrorInternal,
		},
		{
			name: "migration source with run identity",
			mutation: `UPDATE blackboard_changes
				SET kind = 'project_baseline',
					data = json_set(data, '$.source.kind', 'migration', '$.source.name', 'baseline', '$.source.run_id', 'run-legacy')
				WHERE project_id = ?`,
			wantCode: project.ErrorInternal,
		},
		{
			name: "migration missing entity type",
			mutation: `UPDATE blackboard_changes
				SET kind = 'project_baseline', entity_type = '',
					data = json_set(data, '$.source.kind', 'migration', '$.source.name', 'baseline')
				WHERE project_id = ?`,
			wantCode: project.ErrorInternal,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queries, projects, db := newLogTestServices(t)
			created, err := projects.Create(t.Context(), project.CreateProjectRequest{
				Title: "Strict history", Origin: "origin", Goal: "goal",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := projects.UpdateTitle(t.Context(), created.Project.ID, project.UpdateProjectTitleRequest{
				Title: "A valid newer revision", ExpectedRevision: 1,
			}); err != nil {
				t.Fatal(err)
			}
			conn, err := db.GetConn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			args := append(append([]any(nil), test.args...), created.Project.ID)
			if _, err := conn.ExecContext(t.Context(), test.mutation, args...); err != nil {
				conn.Close()
				t.Fatal(err)
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}

			page, err := queries.ListProjectLog(t.Context(), created.Project.ID, ProjectLogQuery{})
			assertCode(t, err, test.wantCode)
			if len(page.Revisions) != 0 {
				t.Fatalf("failed projection returned partial revisions: %+v", page.Revisions)
			}
		})
	}
}
