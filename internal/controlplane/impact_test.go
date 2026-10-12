package controlplane

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestProjectImpactPreviewUsesProductionCoordinationCounts(t *testing.T) {
	application, err := newTestAssembly(
		filepath.Join(t.TempDir(), "aether.db"),
		ControlPlaneHostOptions{OperatorName: "test-operator"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown()

	lease, err := application.ControlPlane.AcquireDispatcherLease(t.Context(), rundomain.DispatcherLeaseRequest{
		InstanceID: "dispatcher-impact", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	startRun := func(meta project.ProjectMeta, task rundomain.TaskType, targetIntentID *string) *rundomain.AdmittedRun {
		t.Helper()
		request := rundomain.StartRequest{
			ProjectID:              meta.ID,
			Task:                   task,
			CandidateGeneration:    meta.Generation,
			CandidateRevision:      meta.Revision,
			TargetIntentID:         targetIntentID,
			WorkKey:                impactWorkKey(meta.ID, task, targetIntentID, meta.Revision),
			DispatcherID:           lease.InstanceID,
			DispatcherFencingToken: lease.FencingToken,
			Spec: rundomain.AdmissionSpecSnapshot{
				ConfigRevision: "impact-v1", PromptTemplateHash: strings.Repeat("a", 64),
				Worker:       rundomain.WorkerSnapshot{Name: "impact-worker", Adapter: "mock"},
				Image:        rundomain.ImageSnapshot{RequestedRef: "worker:v1"},
				Timeouts:     impactTimeouts(task),
				ResultSchema: rundomain.ResultSchemaV1, MaxProposedIntents: impactMaxProposedIntents(task),
			},
			RetryPolicy: rundomain.DefaultRetryPolicy(),
		}
		started, err := application.RunService.StartRun(t.Context(), request)
		if err != nil {
			t.Fatalf("start %s Run: %v", task, err)
		}
		return started
	}

	createProject := func(title string) project.ProjectDetail {
		t.Helper()
		created, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
			Title: title, Origin: "origin", Goal: "goal",
		})
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	createExploreIntent := func(created project.ProjectDetail) (project.ProjectDetail, project.Intent) {
		t.Helper()
		current := created
		tx, err := application.DB.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		result, err := application.ProjectService.ApplyRunResult(t.Context(), tx, project.ApplyRunResultCommand{
			RunID: fmt.Sprintf("impact-proposal-%s", created.Project.ID), ProjectID: created.Project.ID,
			Task: project.RunTaskReason, Generation: created.Project.Generation, InputRevision: created.Project.Revision,
			Worker: "impact-fixture", AppliedAt: time.Now().UTC().Format(time.RFC3339Nano),
			Outcome: project.RunOutcome{Kind: project.RunOutcomeProposeIntents, Intents: []project.ProposedRunIntent{{
				From: []string{"origin"}, Description: "inspect evidence",
			}}},
		})
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		current, err = application.ProjectService.Get(t.Context(), created.Project.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.CreatedIntentIDs) != 1 {
			t.Fatalf("created intent IDs = %v", result.CreatedIntentIDs)
		}
		for _, intent := range current.Intents {
			if intent.ID == result.CreatedIntentIDs[0] {
				return current, intent
			}
		}
		t.Fatalf("created intent %q not found in %+v", result.CreatedIntentIDs[0], current.Intents)
		return project.ProjectDetail{}, project.Intent{}
	}

	hintProject, err := application.ProjectService.Create(t.Context(), project.CreateProjectRequest{
		Title: "impact hint", Origin: "origin", Goal: "goal", BootstrapEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hintProject.Intents) != 1 {
		t.Fatalf("Hint Bootstrap intents = %+v", hintProject.Intents)
	}
	hintTarget := hintProject.Intents[0].ID
	startRun(hintProject.Project, rundomain.TaskBootstrap, &hintTarget)
	preview, err := application.ProjectService.PreviewImpact(t.Context(), hintProject.Project.ID, project.ImpactRequest{
		Action: project.ImpactHintChange, ExpectedRevision: hintProject.Project.Revision,
	})
	if err != nil || preview.AffectedRunCount != 1 {
		t.Fatalf("Hint impact preview = %+v error=%v", preview, err)
	}

	noRunProject := createProject("impact no match")
	preview, err = application.ProjectService.PreviewImpact(t.Context(), noRunProject.Project.ID, project.ImpactRequest{
		Action: project.ImpactHintChange, ExpectedRevision: noRunProject.Project.Revision,
	})
	if err != nil || preview.AffectedRunCount != 0 {
		t.Fatalf("no-match impact preview = %+v error=%v", preview, err)
	}

	factProject, factIntent := createExploreIntent(createProject("impact fact"))
	startRun(factProject.Project, rundomain.TaskReason, nil)
	factTarget := factIntent.ID
	startRun(factProject.Project, rundomain.TaskExplore, &factTarget)
	for _, action := range []project.ImpactAction{project.ImpactRetractFact, project.ImpactSupersedeFact} {
		preview, err = application.ProjectService.PreviewImpact(t.Context(), factProject.Project.ID, project.ImpactRequest{
			Action: action, EntityID: "origin", ExpectedRevision: factProject.Project.Revision,
		})
		if err != nil || preview.AffectedRunCount != 2 {
			t.Fatalf("%s impact preview = %+v error=%v", action, preview, err)
		}
	}

	intentProject, intentTarget := createExploreIntent(createProject("impact intent"))
	intentID := intentTarget.ID
	startRun(intentProject.Project, rundomain.TaskReason, nil)
	startRun(intentProject.Project, rundomain.TaskExplore, &intentID)
	preview, err = application.ProjectService.PreviewImpact(t.Context(), intentProject.Project.ID, project.ImpactRequest{
		Action: project.ImpactCancelIntent, EntityID: intentID, ExpectedRevision: intentProject.Project.Revision,
	})
	if err != nil || preview.AffectedRunCount != 1 {
		t.Fatalf("Intent impact preview = %+v error=%v", preview, err)
	}

	completeProject, completeTarget := createExploreIntent(createProject("impact complete"))
	completeID := completeTarget.ID
	startRun(completeProject.Project, rundomain.TaskReason, nil)
	startRun(completeProject.Project, rundomain.TaskExplore, &completeID)
	preview, err = application.ProjectService.PreviewImpact(t.Context(), completeProject.Project.ID, project.ImpactRequest{
		Action: project.ImpactCompleteProject, ExpectedRevision: completeProject.Project.Revision,
	})
	if err != nil || preview.AffectedRunCount != 2 {
		t.Fatalf("Complete impact preview = %+v error=%v", preview, err)
	}
}

func impactMaxProposedIntents(task rundomain.TaskType) int {
	if task == rundomain.TaskReason {
		return 3
	}
	return 0
}
func impactTimeouts(task rundomain.TaskType) rundomain.TimeoutSnapshot {
	timeouts := rundomain.TimeoutSnapshot{RunSeconds: 120, PhaseSeconds: 10, GraceSeconds: 1}
	if task != rundomain.TaskReason {
		timeouts.FallbackPhaseSeconds = 10
	}
	return timeouts
}
func impactWorkKey(projectID string, task rundomain.TaskType, targetIntentID *string, revision int64) rundomain.WorkKey {
	if targetIntentID != nil {
		return rundomain.WorkKey(fmt.Sprintf("intent:%s:%s:%s", projectID, *targetIntentID, task))
	}
	return rundomain.WorkKey(fmt.Sprintf("reason:%s:g1:r%d", projectID, revision))
}
