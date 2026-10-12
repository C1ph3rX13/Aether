package run_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	projectdomain "github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestFinishRunCommitsOnceAndReplaysSameNormalizedResult(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{Title: "finish", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(ctx, reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	changesBeforeFinish, err := projectService.ListChanges(ctx, project.Project.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(changesBeforeFinish.Changes) != 1 {
		t.Fatalf("Run start leaked into Blackboard History: %+v", changesBeforeFinish)
	}
	firstCandidate := []byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"description":"investigate","from":["origin"]}]}}`)
	receipt, err := runService.FinishRun(ctx, finishRequest(started, firstCandidate))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != rundomain.StatusCommitted || receipt.ProjectRevision != 2 || len(receipt.CreatedIntentIDs) != 1 || receipt.Replayed {
		t.Fatalf("finish receipt: %+v", receipt)
	}

	replayCandidate := []byte(`{"outcome":{"intents":[{"from":["origin"],"description":" investigate "}],"kind":"propose_intents"},"schema":"aether.run-result/v1"}`)
	replayed, err := runService.FinishRun(ctx, finishRequest(started, replayCandidate))
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.ResultHash != receipt.ResultHash || replayed.CreatedIntentIDs[0] != receipt.CreatedIntentIDs[0] {
		t.Fatalf("idempotent receipt: first=%+v replay=%+v", receipt, replayed)
	}

	assertRunErrorCode(t, rundomain.ErrorResultConflict, func() error {
		_, err := runService.FinishRun(ctx, finishRequest(started, reasonNoChangeResult()))
		return err
	})
	detail, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Project.Revision != 2 || len(detail.Intents) != 1 || detail.Intents[0].ID != receipt.CreatedIntentIDs[0] {
		t.Fatalf("idempotent Blackboard: %+v", detail)
	}
	assertRunAfterImage(t, projectService, project.Project.ID, string(started.Run.ID), started.Run.Spec.Worker.Name, projectdomain.ChangeIntentsProposed, 2)
	conn, err := runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var claims, checkpointRevision int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM execution_claims").Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx,
		"SELECT input_revision FROM reason_checkpoints WHERE project_id = ? AND generation = 1", project.Project.ID,
	).Scan(&checkpointRevision); err != nil {
		t.Fatal(err)
	}
	if claims != 0 || checkpointRevision != 1 {
		t.Fatalf("finish coordination: claims=%d checkpoint=%d", claims, checkpointRevision)
	}
	conn.Close()
	stored, err := runService.GetRun(ctx, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != rundomain.StatusCommitted || stored.ResultHash != receipt.ResultHash || stored.OutcomeKind != rundomain.OutcomeProposeIntents {
		t.Fatalf("stored run: %+v", stored)
	}
}

func TestFinishRunFencesSupersededAndWrongTokens(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{Title: "fence", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 11, 0, 0, 0, time.UTC)
	runService.clock = func() time.Time { return now }
	request := reasonStartRequest(project.Project)
	first, err := runService.StartRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		"UPDATE execution_claims SET lease_expires_at = '2000-01-01T00:00:00Z' WHERE run_id = ?", first.Run.ID,
	); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	assertRunErrorCode(t, rundomain.ErrorRetryDeferred, func() error {
		_, err := runService.StartRun(ctx, request)
		return err
	})
	storedFirst, err := runService.GetRun(ctx, first.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedFirst.Status != rundomain.StatusInterrupted || storedFirst.NextEligibleAt == nil {
		t.Fatalf("expired first Run: %+v", storedFirst)
	}
	now = *storedFirst.NextEligibleAt
	second, err := runService.StartRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Run.FencingToken <= first.Run.FencingToken {
		t.Fatalf("fencing did not advance: first=%d second=%d", first.Run.FencingToken, second.Run.FencingToken)
	}
	assertRunErrorCode(t, rundomain.ErrorResultConflict, func() error {
		_, err := runService.FinishRun(ctx, finishRequest(first, reasonNoChangeResult()))
		return err
	})
	wrong := finishRequest(second, reasonNoChangeResult())
	wrong.FencingToken = first.Run.FencingToken
	assertRunErrorCode(t, rundomain.ErrorFenced, func() error {
		_, err := runService.FinishRun(ctx, wrong)
		return err
	})
	if _, err := runService.FinishRun(ctx, finishRequest(second, reasonNoChangeResult())); err != nil {
		t.Fatal(err)
	}
	storedFirst, err = runService.GetRun(ctx, first.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedFirst.Status != rundomain.StatusInterrupted {
		t.Fatalf("fenced run status: %+v", storedFirst)
	}
}

func TestFinishRunMarksStaleReasonWithoutBlackboardWrite(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{Title: "stale", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(ctx, reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projectService.UpdateTitle(ctx, project.Project.ID, projectdomain.UpdateProjectTitleRequest{
		Title: "new title", ExpectedRevision: project.Project.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	receipt, err := runService.FinishRun(ctx, finishRequest(started, reasonNoChangeResult()))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != rundomain.StatusStale || receipt.Code != rundomain.ErrorStaleInput || receipt.ProjectRevision != 2 {
		t.Fatalf("stale receipt: %+v", receipt)
	}
	replayed, err := runService.FinishRun(ctx, finishRequest(started, reasonNoChangeResult()))
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Status != rundomain.StatusStale {
		t.Fatalf("stale replay: %+v", replayed)
	}
	detail, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Project.Revision != 2 || detail.Project.Title != "new title" || len(detail.Hints) != 0 || len(detail.Intents) != 0 {
		t.Fatalf("stale result changed Blackboard: %+v", detail)
	}
	stored, err := runService.GetRun(ctx, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != rundomain.StatusStale || stored.ErrorCode != rundomain.ErrorStaleInput {
		t.Fatalf("stale run audit: %+v", stored)
	}
	conn, err := runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var claims, checkpoints int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM execution_claims").Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM reason_checkpoints").Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if claims != 0 || checkpoints != 0 {
		t.Fatalf("stale coordination: claims=%d checkpoints=%d", claims, checkpoints)
	}
}

func TestFinishRunExploreAllowsUnrelatedRevisionChange(t *testing.T) {
	projectService, runService, project, intent, started, cleanup := startIntentRun(t, rundomain.TaskExplore)
	defer cleanup()
	ctx := t.Context()
	if _, err := projectService.CreateHint(ctx, project.Project.ID, CreateHintRequest{Content: "parallel hint", ExpectedRevision: project.Project.Revision}); err != nil {
		t.Fatal(err)
	}
	candidate := []byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"found evidence"}}}`)
	receipt, err := runService.FinishRun(ctx, finishRequest(started, candidate))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != rundomain.StatusCommitted || receipt.ProjectRevision != 4 || len(receipt.CreatedFactIDs) != 1 {
		t.Fatalf("explore receipt: %+v", receipt)
	}
	detail, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotIntent := findIntent(detail.Intents, intent.ID)
	if gotIntent == nil || gotIntent.To == nil || *gotIntent.To != receipt.CreatedFactIDs[0] {
		t.Fatalf("concluded intent: %+v", gotIntent)
	}
	assertRunAfterImage(t, projectService, project.Project.ID, string(started.Run.ID), started.Run.Spec.Worker.Name, projectdomain.ChangeIntentConcluded, 4)
}

func TestFinishRunExploreCorrectionCancelsCommitRights(t *testing.T) {
	projectService, runService, project, intent, started, cleanup := startIntentRun(t, rundomain.TaskExplore)
	defer cleanup()
	if _, err := projectService.CancelIntent(t.Context(), project.Project.ID, intent.ID, CancelIntentRequest{
		Reason: "target is no longer useful", ExpectedRevision: project.Project.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	receipt, err := runService.FinishRun(t.Context(), finishRequest(started, []byte(
		`{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"late result"}}}`,
	)))
	if receipt != nil {
		t.Fatalf("cancelled Explore returned a receipt: %+v", receipt)
	}
	var domainErr *rundomain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != rundomain.ErrorResultConflict {
		t.Fatalf("cancelled Explore finish error = %v, want %s", err, rundomain.ErrorResultConflict)
	}
	detail, err := projectService.Get(t.Context(), project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := findIntent(detail.Intents, intent.ID)
	if detail.Project.Revision != 3 || target == nil || target.CancelledAt == nil || len(detail.Facts) != 2 {
		t.Fatalf("cancelled Explore changed Blackboard unexpectedly: %+v", detail)
	}
	stored, err := runService.GetRun(t.Context(), started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != rundomain.StatusCancelled || stored.ErrorCode != rundomain.ErrorIntentCancelled {
		t.Fatalf("cancelled Explore run = %+v", stored)
	}
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var claims int
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM execution_claims WHERE run_id = ?", started.Run.ID).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if claims != 0 {
		t.Fatalf("cancelled Explore claims = %d, want 0", claims)
	}
}

func TestFinishRunFailureRollsBackExploreBusinessState(t *testing.T) {
	projectService, runService, project, intent, started, cleanup := startIntentRun(t, rundomain.TaskExplore)
	defer cleanup()
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		CREATE TRIGGER reject_test_explore_finish
		BEFORE UPDATE OF status ON execution_runs
		WHEN NEW.status = 'committed'
		BEGIN
			SELECT RAISE(ABORT, 'reject test Explore finish');
		END`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()
	if _, err := runService.FinishRun(t.Context(), finishRequest(started, []byte(
		`{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"explore result"}}}`,
	))); err == nil {
		t.Fatal("expected injected Explore finish failure")
	}
	detail, err := projectService.Get(t.Context(), project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := findIntent(detail.Intents, intent.ID)
	stored, err := runService.GetRun(t.Context(), started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Project.Revision != 2 || len(detail.Facts) != 2 || target == nil || !target.IsOpen() || stored.Status != rundomain.StatusActive {
		t.Fatalf("failed Explore finish leaked state: detail=%+v run=%+v", detail, stored)
	}
	replay, err := projectService.ListChanges(t.Context(), project.Project.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	conn, err = runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var claims int
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM execution_claims WHERE run_id = ?", started.Run.ID).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if len(replay.Changes) != 0 || claims != 1 {
		t.Fatalf("failed Explore finish leaked History or Claim: replay=%+v claims=%d", replay, claims)
	}
}

func TestFinishRunReasonCompletionCreatesImmutableCompletion(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{Title: "complete", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(ctx, reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	candidate := []byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"goal satisfied","from":["origin"]}}`)
	receipt, err := runService.FinishRun(ctx, finishRequest(started, candidate))
	if err != nil {
		t.Fatal(err)
	}
	detail, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != rundomain.StatusCommitted || receipt.CompletionIntentID == "" ||
		detail.Project.Status != ProjectStatusCompleted || detail.Project.Revision != 2 ||
		len(detail.CompletionRecords) != 1 || len(detail.Intents) != 1 || detail.Intents[0].To == nil || *detail.Intents[0].To != "goal" {
		t.Fatalf("Reason completion: receipt=%+v detail=%+v", receipt, detail)
	}
	assertRunAfterImage(t, projectService, project.Project.ID, string(started.Run.ID), started.Run.Spec.Worker.Name, projectdomain.ChangeProjectCompleted, 2)
}

func TestFinishRunDeclineReleasesClaimWithoutBlackboardChange(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{Title: "decline", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(ctx, reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	candidate := []byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"not my domain"}}`)
	receipt, err := runService.FinishRun(ctx, finishRequest(started, candidate))
	if err != nil {
		t.Fatal(err)
	}
	detail, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var claims, checkpoints int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM execution_claims").Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM reason_checkpoints").Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != rundomain.StatusDeclined || detail.Project.Revision != 1 ||
		len(detail.Intents) != 0 || claims != 0 || checkpoints != 0 {
		t.Fatalf("decline result: receipt=%+v detail=%+v claims=%d checkpoints=%d", receipt, detail, claims, checkpoints)
	}
}

func TestFinishRunBootstrapFallbackConcludesWithoutCompleting(t *testing.T) {
	projectService, runService, project, intent, started, cleanup := startIntentRun(t, rundomain.TaskBootstrap)
	defer cleanup()
	ctx := t.Context()
	candidate := []byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"conclude_intent","fact":{"description":"partial evidence"}}}`)
	receipt, err := runService.FinishRun(ctx, finishRequest(started, candidate))
	if err != nil {
		t.Fatal(err)
	}
	detail, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := findIntent(detail.Intents, intent.ID)
	if receipt.Status != rundomain.StatusCommitted || detail.Project.Status != ProjectStatusActive ||
		detail.Project.Revision != 2 || len(detail.CompletionRecords) != 0 ||
		target == nil || target.To == nil || *target.To != receipt.CreatedFactIDs[0] {
		t.Fatalf("Bootstrap fallback: receipt=%+v detail=%+v", receipt, detail)
	}
	assertRunAfterImage(t, projectService, project.Project.ID, string(started.Run.ID), started.Run.Spec.Worker.Name, projectdomain.ChangeIntentConcluded, 2)
}

func TestFinishRunBootstrapCompletionIsAtomic(t *testing.T) {
	projectService, runService, project, intent, started, cleanup := startIntentRun(t, rundomain.TaskBootstrap)
	defer cleanup()
	ctx := t.Context()
	candidate := bootstrapCompleteResult()
	receipt, err := runService.FinishRun(ctx, finishRequest(started, candidate))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != rundomain.StatusCommitted || len(receipt.CreatedFactIDs) != 1 || receipt.CompletionIntentID == "" {
		t.Fatalf("bootstrap receipt: %+v", receipt)
	}
	detail, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := findIntent(detail.Intents, intent.ID)
	if detail.Project.Status != ProjectStatusCompleted || detail.Project.Revision != 2 ||
		target == nil || target.To == nil || *target.To != receipt.CreatedFactIDs[0] || len(detail.CompletionRecords) != 1 {
		t.Fatalf("atomic bootstrap completion: %+v", detail)
	}
	replayed, err := runService.FinishRun(t.Context(), finishRequest(started, candidate))
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.ProjectRevision != receipt.ProjectRevision || replayed.CreatedFactIDs[0] != receipt.CreatedFactIDs[0] {
		t.Fatalf("bootstrap replay: first=%+v replay=%+v", receipt, replayed)
	}
	assertRunAfterImage(t, projectService, project.Project.ID, string(started.Run.ID), started.Run.Spec.Worker.Name, projectdomain.ChangeBootstrapCompleted, 2)
}

func assertRunAfterImage(
	t *testing.T,
	service *ProjectService,
	projectID, runID, worker string,
	kind projectdomain.ChangeKind,
	revision int64,
) {
	t.Helper()
	replay, err := service.ListChanges(t.Context(), projectID, revision-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Changes) != 1 {
		t.Fatalf("Run change count at revision %d: %+v", revision, replay)
	}
	change := replay.Changes[0]
	var payload struct {
		SchemaVersion int                        `json:"schema_version"`
		Source        projectdomain.ChangeSource `json:"source"`
		Before        ProjectDetail              `json:"before"`
		After         ProjectDetail              `json:"after"`
	}
	if err := json.Unmarshal(change.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if change.Kind != kind || change.Revision != revision || payload.SchemaVersion != projectdomain.ChangeSchemaVersion ||
		payload.Source.Kind != "run" || payload.Source.Name != worker || payload.Source.RunID == nil || *payload.Source.RunID != runID ||
		payload.Before.Project.Revision != revision-1 || payload.After.Project.Revision != revision {
		t.Fatalf("Run after-image at revision %d: change=%+v payload=%+v", revision, change, payload)
	}
	var delta struct {
		SchemaVersion int                      `json:"schema_version"`
		Kind          projectdomain.ChangeKind `json:"kind"`
		Facts         []struct {
			ID string `json:"id"`
		} `json:"facts"`
		Intents []struct {
			ID string `json:"id"`
		} `json:"intents"`
		Completions []struct {
			IntentID string `json:"id"`
		} `json:"completions"`
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(change.Delta, &delta); err != nil {
		t.Fatal(err)
	}
	if delta.SchemaVersion != projectdomain.ChangeSchemaVersion || delta.Kind != kind {
		t.Fatalf("typed Run delta at revision %d: %+v", revision, delta)
	}
	containsID := func(values []struct {
		ID string `json:"id"`
	}, want string) bool {
		for _, value := range values {
			if value.ID == want {
				return true
			}
		}
		return false
	}
	resultID := func(key string) string {
		value, _ := delta.Result[key].(string)
		return value
	}
	switch kind {
	case projectdomain.ChangeIntentsProposed:
		intentIDs, ok := delta.Result["intent_ids"].([]any)
		identitiesMatch := ok && len(intentIDs) == len(delta.Intents)
		for _, value := range intentIDs {
			intentID, stringOK := value.(string)
			identitiesMatch = identitiesMatch && stringOK && containsID(delta.Intents, intentID)
		}
		if change.EntityType != "intent_batch" || !identitiesMatch {
			t.Fatalf("proposed Intent delta identity: change=%+v delta=%+v", change, delta)
		}
	case projectdomain.ChangeIntentConcluded:
		factID := resultID("fact_id")
		if change.EntityType != "intent" || change.EntityID == nil || len(delta.Facts) != 1 ||
			len(delta.Intents) != 1 || delta.Intents[0].ID != *change.EntityID || factID == "" || delta.Facts[0].ID != factID {
			t.Fatalf("concluded Intent delta identity: change=%+v delta=%+v", change, delta)
		}
	case projectdomain.ChangeProjectCompleted:
		completionID := resultID("completion_intent_id")
		if change.EntityType != "project" || change.EntityID == nil || *change.EntityID != projectID ||
			len(delta.Intents) != 1 || len(delta.Completions) != 1 || completionID == "" ||
			delta.Intents[0].ID != completionID || delta.Completions[0].IntentID != completionID {
			t.Fatalf("Reason completion delta identity: change=%+v delta=%+v", change, delta)
		}
	case projectdomain.ChangeBootstrapCompleted:
		factID, targetID, completionID := resultID("fact_id"), resultID("target_intent_id"), resultID("completion_intent_id")
		if change.EntityType != "project" || change.EntityID == nil || *change.EntityID != projectID ||
			len(delta.Facts) != 1 || delta.Facts[0].ID != factID || len(delta.Intents) != 2 ||
			!containsID(delta.Intents, targetID) || !containsID(delta.Intents, completionID) ||
			len(delta.Completions) != 1 || delta.Completions[0].IntentID != completionID ||
			factID == "" || targetID == "" || completionID == "" {
			t.Fatalf("Bootstrap completion delta identity: change=%+v delta=%+v", change, delta)
		}
	}
}

func TestFinishRunBootstrapEligibilityLossBecomesStale(t *testing.T) {
	projectService, runService, project, intent, started, cleanup := startIntentRun(t, rundomain.TaskBootstrap)
	defer cleanup()
	ctx := t.Context()
	conn, err := runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "UPDATE projects SET bootstrap_enabled = 0 WHERE id = ?", project.Project.ID); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	receipt, err := runService.FinishRun(ctx, finishRequest(started, bootstrapCompleteResult()))
	if err != nil {
		t.Fatal(err)
	}
	detail, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := findIntent(detail.Intents, intent.ID)
	if receipt.Status != rundomain.StatusStale || detail.Project.Status != ProjectStatusActive ||
		detail.Project.Revision != 1 || len(detail.Facts) != 2 || target == nil || !target.IsOpen() {
		t.Fatalf("bootstrap eligibility loss: receipt=%+v detail=%+v", receipt, detail)
	}
}

func TestFinishRunFailureRollsBackBootstrapBusinessState(t *testing.T) {
	projectService, runService, project, intent, started, cleanup := startIntentRun(t, rundomain.TaskBootstrap)
	defer cleanup()
	ctx := t.Context()
	conn, err := runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
		CREATE TRIGGER reject_test_run_finish
		BEFORE UPDATE OF status ON execution_runs
		WHEN NEW.status = 'committed'
		BEGIN
			SELECT RAISE(ABORT, 'reject test finish');
		END`); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if _, err := runService.FinishRun(ctx, finishRequest(started, bootstrapCompleteResult())); err == nil {
		t.Fatal("expected injected finish failure")
	}
	detail, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := findIntent(detail.Intents, intent.ID)
	if detail.Project.Status != ProjectStatusActive || detail.Project.Revision != 1 ||
		len(detail.Facts) != 2 || len(detail.CompletionRecords) != 0 || target == nil || !target.IsOpen() {
		t.Fatalf("failed finish leaked business state: %+v", detail)
	}
	stored, err := runService.GetRun(ctx, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != rundomain.StatusActive {
		t.Fatalf("failed finish changed run: %+v", stored)
	}
}

func TestFinishRunFailureDoesNotPublishProjectChange(t *testing.T) {
	publisher := &recordingRunChangePublisher{}
	projectService, runService, cleanup := newRunTestServicesWithPublisher(t, publisher)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{Title: "publish rollback", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(t.Context(), reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := runService.DB.GetConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `
		CREATE TRIGGER reject_test_run_finish_publication
		BEFORE UPDATE OF status ON execution_runs
		WHEN NEW.status = 'committed'
		BEGIN
			SELECT RAISE(ABORT, 'reject test finish publication');
		END`); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()

	if _, err := runService.FinishRun(t.Context(), finishRequest(started, []byte(
		`{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"description":"publish only after commit","from":["origin"]}]}}`,
	))); err == nil {
		t.Fatal("expected injected finish failure")
	}
	if len(publisher.changes) != 0 {
		t.Fatalf("failed Finish Run published Project change: %+v", publisher.changes)
	}
	detail, err := projectService.Get(t.Context(), project.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Project.Revision != 1 || len(detail.Intents) != 0 {
		t.Fatalf("failed Finish Run leaked Blackboard state: %+v", detail)
	}
}

func TestFinishRunPublishesProjectChangeAfterAtomicCommit(t *testing.T) {
	publisher := &recordingRunChangePublisher{}
	projectService, runService, cleanup := newRunTestServicesWithPublisher(t, publisher)
	defer cleanup()
	project, err := projectService.Create(t.Context(), CreateProjectRequest{Title: "publish commit", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(t.Context(), reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	var observedRevision int64
	var observedRunStatus rundomain.Status
	publisher.observe = func(change rundomain.ProjectChange) error {
		detail, err := projectService.Get(context.Background(), change.ProjectID)
		if err != nil {
			return err
		}
		stored, err := runService.GetRun(context.Background(), started.Run.ID)
		if err != nil {
			return err
		}
		observedRevision = detail.Project.Revision
		observedRunStatus = stored.Status
		return nil
	}
	receipt, err := runService.FinishRun(t.Context(), finishRequest(started, []byte(
		`{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"description":"publish committed state","from":["origin"]}]}}`,
	)))
	if err != nil {
		t.Fatal(err)
	}
	if len(publisher.changes) != 1 || publisher.err != nil ||
		publisher.changes[0].Revision != receipt.ProjectRevision ||
		observedRevision != receipt.ProjectRevision || observedRunStatus != rundomain.StatusCommitted {
		t.Fatalf(
			"post-commit publication: changes=%+v error=%v revision=%d run_status=%s receipt=%+v",
			publisher.changes, publisher.err, observedRevision, observedRunStatus, receipt,
		)
	}
}

func TestFinishRunInvalidResultLeavesRunAndClaimActive(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := t.Context()
	project, err := projectService.Create(ctx, CreateProjectRequest{Title: "invalid", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(ctx, reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	assertRunErrorCode(t, rundomain.ErrorInvalidResult, func() error {
		_, err := runService.FinishRun(ctx, finishRequest(started, []byte(`{"accepted":true,"data":{}}`)))
		return err
	})
	assertRunErrorCode(t, rundomain.ErrorInvalidResult, func() error {
		_, err := runService.FinishRun(ctx, finishRequest(started, []byte(
			`{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"description":"invalid source","from":["f999"]}]}}`,
		)))
		return err
	})
	stored, err := runService.GetRun(ctx, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var claims int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM execution_claims WHERE run_id = ?", started.Run.ID).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if stored.Status != rundomain.StatusActive || claims != 1 {
		t.Fatalf("invalid result mutated execution right: run=%+v claims=%d", stored, claims)
	}
}

func TestFinishRunConcurrentDifferentResultsHasOneWinner(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	ctx := context.Background()
	project, err := projectService.Create(ctx, CreateProjectRequest{Title: "race", Origin: "origin", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(ctx, reasonStartRequest(project.Project))
	if err != nil {
		t.Fatal(err)
	}
	candidates := [][]byte{
		reasonNoChangeResult(),
		[]byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"propose_intents","intents":[{"description":"one","from":["origin"]}]}}`),
	}
	start := make(chan struct{})
	results := make(chan error, len(candidates))
	var wait sync.WaitGroup
	for _, candidate := range candidates {
		candidate := candidate
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := runService.FinishRun(ctx, finishRequest(started, candidate))
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		var domainErr *rundomain.DomainError
		if errors.As(err, &domainErr) && domainErr.Code == rundomain.ErrorResultConflict {
			conflicts++
			continue
		}
		t.Fatalf("unexpected concurrent finish error: %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent finishes: successes=%d conflicts=%d", successes, conflicts)
	}
	conn, err := runService.DB.GetConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var claims int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM execution_claims").Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if claims != 0 {
		t.Fatalf("winning finish did not release claim: %d", claims)
	}
}

func TestFinishRunPersistsTechnicalTerminationsAndReleasesClaim(t *testing.T) {
	tests := []struct {
		name   string
		status rundomain.Status
		code   rundomain.ErrorCode
	}{
		{name: "failed", status: rundomain.StatusFailed, code: rundomain.ExecutionProcessFailed},
		{name: "cancelled", status: rundomain.StatusCancelled},
		{name: "interrupted", status: rundomain.StatusInterrupted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectService, runService, cleanup := newRunTestServices(t)
			defer cleanup()
			project, err := projectService.Create(t.Context(), CreateProjectRequest{
				Title: test.name, Origin: "origin", Goal: "goal",
			})
			if err != nil {
				t.Fatal(err)
			}
			started, err := runService.StartRun(t.Context(), reasonStartRequest(project.Project))
			if err != nil {
				t.Fatal(err)
			}
			request := rundomain.FinishRequest{
				RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
				Termination: &rundomain.RunTermination{Status: test.status, Code: test.code},
			}
			receipt, err := runService.FinishRun(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.Status != test.status || receipt.Code != test.code || receipt.ProjectRevision != 1 {
				t.Fatalf("termination receipt: %+v", receipt)
			}
			replayed, err := runService.FinishRun(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if !replayed.Replayed || replayed.Status != test.status || replayed.Code != test.code {
				t.Fatalf("termination replay: %+v", replayed)
			}
			conflict := request
			conflict.Termination = &rundomain.RunTermination{Status: rundomain.StatusFailed, Code: rundomain.ExecutionCaptureFailed}
			if test.status == rundomain.StatusFailed && test.code == rundomain.ExecutionCaptureFailed {
				conflict.Termination.Code = rundomain.ExecutionLaunchFailed
			}
			assertRunErrorCode(t, rundomain.ErrorResultConflict, func() error {
				_, err := runService.FinishRun(t.Context(), conflict)
				return err
			})
			stored, err := runService.GetRun(t.Context(), started.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != test.status || stored.ErrorCode != test.code || stored.ResultHash != "" || stored.OutcomeKind != "" {
				t.Fatalf("stored termination: %+v", stored)
			}
			detail, err := projectService.Get(t.Context(), project.Project.ID)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := runService.DB.GetConn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			var claims int
			if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM execution_claims").Scan(&claims); err != nil {
				t.Fatal(err)
			}
			if detail.Project.Revision != 1 || claims != 0 {
				t.Fatalf("termination changed business state or retained claim: detail=%+v claims=%d", detail, claims)
			}
		})
	}
}

func TestFinishRunPersistsCaptureFailureReasonThroughActivityProjections(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "capture failure", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(t.Context(), reasonStartRequest(created.Project))
	if err != nil {
		t.Fatal(err)
	}
	reason := rundomain.CaptureFailureMultipleCandidates
	diagnostic := rundomain.CaptureDiagnosticResultFileInvalid
	request := rundomain.FinishRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		CaptureDiagnostic: diagnostic,
		Termination: &rundomain.RunTermination{
			Status: rundomain.StatusFailed, Code: rundomain.ExecutionCaptureFailed,
			CaptureFailureReason: reason,
		},
	}
	receipt, err := runService.FinishRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.CaptureFailureReason != reason || receipt.CaptureDiagnostic != diagnostic || receipt.Code != rundomain.ExecutionCaptureFailed {
		t.Fatalf("capture failure receipt: %+v", receipt)
	}
	stored, err := runService.GetRun(t.Context(), started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CaptureFailureReason != reason || stored.CaptureDiagnostic != diagnostic {
		t.Fatalf("stored capture diagnostics: reason=%q diagnostic=%q", stored.CaptureFailureReason, stored.CaptureDiagnostic)
	}
	page, err := runService.ListProjectActivity(t.Context(), created.Project.ID, rundomain.ActivityQuery{View: rundomain.ActivityFinished})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 1 || page.Runs[0].CaptureFailureReason != reason || page.Runs[0].CaptureDiagnostic != diagnostic {
		t.Fatalf("Activity projection: %+v", page.Runs)
	}
	detail, err := runService.GetProjectActivityRunDetail(t.Context(), created.Project.ID, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.CaptureFailureReason != reason || detail.CaptureDiagnostic != diagnostic || detail.ErrorCode != rundomain.ExecutionCaptureFailed {
		t.Fatalf("Activity detail: %+v", detail)
	}
	chain, err := runService.GetProjectRetryChain(t.Context(), created.Project.ID, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 1 || chain[0].CaptureFailureReason != reason || chain[0].CaptureDiagnostic != diagnostic {
		t.Fatalf("retry chain projection: %+v", chain)
	}
}

func TestFinishRunPersistsResultEvidenceSource(t *testing.T) {
	tests := []struct {
		name    string
		source  rundomain.ResultEvidenceSource
		stale   bool
		decline bool
	}{
		{name: "committed", source: rundomain.ResultEvidenceSourceFile},
		{name: "declined", source: rundomain.ResultEvidenceSourceFile, decline: true},
		{name: "stale", source: rundomain.ResultEvidenceSourceFile, stale: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectService, runService, cleanup := newRunTestServices(t)
			defer cleanup()
			created, err := projectService.Create(t.Context(), CreateProjectRequest{
				Title: test.name + " result", Origin: "origin", Goal: "goal",
			})
			if err != nil {
				t.Fatal(err)
			}
			request := reasonStartRequest(created.Project)
			started, err := runService.StartRun(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			candidate := reasonNoChangeResult()
			if test.decline {
				candidate = []byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"decline","reason":"not supported"}}`)
			}
			if test.stale {
				if _, err := projectService.UpdateTitle(t.Context(), created.Project.ID, projectdomain.UpdateProjectTitleRequest{
					Title: "new title", ExpectedRevision: created.Project.Revision,
				}); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := runService.FinishRun(t.Context(), rundomain.FinishRequest{
				RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
				Candidate: candidate, ResultEvidenceSource: test.source,
			})
			if err != nil {
				t.Fatal(err)
			}
			if receipt.ResultEvidenceSource != test.source {
				t.Fatalf("receipt source=%q, want %q", receipt.ResultEvidenceSource, test.source)
			}
			stored, err := runService.GetRun(t.Context(), started.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.ResultEvidenceSource != test.source {
				t.Fatalf("stored source=%q, want %q", stored.ResultEvidenceSource, test.source)
			}
			page, err := runService.ListProjectActivity(t.Context(), created.Project.ID, rundomain.ActivityQuery{View: rundomain.ActivityFinished})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Runs) != 1 || page.Runs[0].ResultEvidenceSource != test.source {
				t.Fatalf("Activity source: %+v", page.Runs)
			}
			detail, err := runService.GetProjectActivityRunDetail(t.Context(), created.Project.ID, started.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if detail.ResultEvidenceSource != test.source {
				t.Fatalf("Activity detail source=%q, want %q", detail.ResultEvidenceSource, test.source)
			}
			chain, err := runService.GetProjectRetryChain(t.Context(), created.Project.ID, started.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(chain) != 1 || chain[0].ResultEvidenceSource != test.source {
				t.Fatalf("Retry Chain source: %+v", chain)
			}
		})
	}
}

func TestFinishRunLeavesNormallyParsedCandidateDiagnosticEmpty(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "normal candidate", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(t.Context(), reasonStartRequest(created.Project))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := runService.FinishRun(t.Context(), rundomain.FinishRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		Candidate: reasonNoChangeResult(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.CaptureDiagnostic != "" || receipt.ResultEvidenceSource != "" {
		t.Fatalf("normal Finish receipt evidence: diagnostic=%q source=%q", receipt.CaptureDiagnostic, receipt.ResultEvidenceSource)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["capture_diagnostic"]; present {
		t.Fatalf("normal Finish receipt exposed an empty capture diagnostic: %s", encoded)
	}
	if _, present := fields["result_evidence_source"]; present {
		t.Fatalf("normal Finish receipt exposed an empty result evidence source: %s", encoded)
	}
	stored, err := runService.GetRun(t.Context(), started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CaptureDiagnostic != "" || stored.ResultEvidenceSource != "" {
		t.Fatalf("normal stored evidence: diagnostic=%q source=%q", stored.CaptureDiagnostic, stored.ResultEvidenceSource)
	}
}

func TestFinishRunRejectsResultFileDiagnosticOnTechnicalTermination(t *testing.T) {
	projectService, runService, cleanup := newRunTestServices(t)
	defer cleanup()
	created, err := projectService.Create(t.Context(), CreateProjectRequest{
		Title: "invalid repair evidence", Origin: "origin", Goal: "goal",
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runService.StartRun(t.Context(), reasonStartRequest(created.Project))
	if err != nil {
		t.Fatal(err)
	}
	assertRunErrorCode(t, rundomain.ErrorInvalidArgument, func() error {
		_, err := runService.FinishRun(t.Context(), rundomain.FinishRequest{
			RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
			CaptureDiagnostic: rundomain.CaptureDiagnosticResultFileInvalid,
			Termination: &rundomain.RunTermination{
				Status: rundomain.StatusFailed, Code: rundomain.ExecutionProcessFailed,
			},
		})
		return err
	})
}

func TestCompletionFinishRequestCarriesFileEvidenceAtTopLevel(t *testing.T) {
	completion := rundomain.Completion{
		RunID: "run-1", FencingToken: 2, Kind: rundomain.CompletionResultReady,
		Candidate:            reasonNoChangeResult(),
		ResultEvidenceSource: rundomain.ResultEvidenceSourceFile,
	}
	request, err := completion.FinishRequest()
	if err != nil {
		t.Fatal(err)
	}
	if request.CaptureDiagnostic != "" ||
		request.ResultEvidenceSource != rundomain.ResultEvidenceSourceFile || request.Termination != nil {
		t.Fatalf("candidate metadata was not carried at FinishRequest top level: %+v", request)
	}
}

func finishRequest(started *rundomain.AdmittedRun, candidate []byte) rundomain.FinishRequest {
	return rundomain.FinishRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken, Candidate: candidate,
	}
}

func reasonNoChangeResult() []byte {
	return []byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`)
}

func bootstrapCompleteResult() []byte {
	return []byte(`{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"goal reached","fact":{"description":"final answer"}}}`)
}

type recordingRunChangePublisher struct {
	changes []rundomain.ProjectChange
	observe func(rundomain.ProjectChange) error
	err     error
}

func (publisher *recordingRunChangePublisher) PublishProjectChange(change rundomain.ProjectChange) {
	publisher.changes = append(publisher.changes, change)
	if publisher.observe != nil {
		publisher.err = publisher.observe(change)
	}
}

func startIntentRun(
	t *testing.T,
	task rundomain.TaskType,
) (*ProjectService, *RunService, ProjectDetail, Intent, *rundomain.AdmittedRun, func()) {
	t.Helper()
	projectService, runService, cleanup := newRunTestServices(t)
	ctx := t.Context()
	createRequest := CreateProjectRequest{Title: string(task), Origin: "origin", Goal: "goal"}
	if task == rundomain.TaskBootstrap {
		createRequest.BootstrapEnabled = true
	}
	project, err := projectService.Create(ctx, createRequest)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	var targetIntent Intent
	if task == rundomain.TaskBootstrap {
		if len(project.Intents) != 1 || !schedulingIsBootstrapIntent(project.Intents[0]) {
			cleanup()
			t.Fatalf("materialized Bootstrap intent: %+v", project.Intents)
		}
		targetIntent = project.Intents[0]
	} else {
		targetIntent = proposeIntent(t, projectService, runService, project.Project.ID, "explore target", []string{"origin"})
	}
	current, err := projectService.Get(ctx, project.Project.ID)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	target := targetIntent.ID
	request := reasonStartRequest(current.Project)
	setStartRequestTarget(&request, task, target)
	request.Spec.MaxProposedIntents = 0
	if task == rundomain.TaskBootstrap || task == rundomain.TaskExplore {
		request.Spec.Timeouts.FallbackPhaseSeconds = request.Spec.Timeouts.PhaseSeconds
		request.Spec.Timeouts.RunSeconds = 3 * (request.Spec.Timeouts.PhaseSeconds + request.Spec.Timeouts.GraceSeconds)
	}
	started, err := runService.StartRun(ctx, request)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	return projectService, runService, current, targetIntent, started, cleanup
}

func findIntent(intents []Intent, intentID string) *Intent {
	for index := range intents {
		if intents[index].ID == intentID {
			return &intents[index]
		}
	}
	return nil
}
