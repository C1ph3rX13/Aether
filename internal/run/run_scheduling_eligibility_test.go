package run

import "testing"

func TestReasonChangeRangeEligibilityRequiresExplicitChangePolicy(t *testing.T) {
	query := SchedulingWorkQuery{Generation: 2, HasOpenIntents: true}
	nonTriggering := []ProjectChangeKind{
		ProjectChangeProjectBaseline,
		ProjectChangeProjectCreated,
		ProjectChangeProjectRenamed,
		ProjectChangeProjectPaused,
		ProjectChangeProjectResumed,
		ProjectChangeIntentCreated,
		ProjectChangeProjectCompleted,
		ProjectChangeProjectReopened,
		ProjectChangeIntentsProposed,
	}
	changes := make([]ProjectChange, len(nonTriggering))
	for index, kind := range nonTriggering {
		changes[index] = ProjectChange{Generation: query.Generation, Kind: kind}
	}
	if reasonChangeRangeEligible(query, ProjectChangeRange{Changes: changes}) {
		t.Fatal("explicitly non-triggering changes made Reason eligible")
	}
	if !reasonChangeRangeEligible(query, ProjectChangeRange{Changes: []ProjectChange{{
		Generation: query.Generation, Kind: ProjectChangeKind("future_change_kind"),
	}}}) {
		t.Fatal("unknown change kind suppressed Reason")
	}
	if reasonChangeRangeEligible(query, ProjectChangeRange{Changes: []ProjectChange{{
		Generation: query.Generation, Kind: ProjectChangeIntentCancelled,
	}}}) {
		t.Fatal("Intent cancellation triggered Reason while ordinary Intents remain open")
	}
	query.HasOpenIntents = false
	if !reasonChangeRangeEligible(query, ProjectChangeRange{Changes: []ProjectChange{{
		Generation: query.Generation, Kind: ProjectChangeIntentCancelled,
	}}}) {
		t.Fatal("last Intent cancellation did not trigger Reason")
	}
}
