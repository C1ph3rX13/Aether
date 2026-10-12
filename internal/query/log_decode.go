package query

import (
	"fmt"
	"sort"
	"strings"

	"github.com/C1ph3rX13/Aether/internal/project"
)

func decodeLogRevision(change project.HistoryRevision) (LogRevision, error) {
	source, err := decodeLogSource(change.Source)
	if err != nil {
		return LogRevision{}, err
	}
	if migration, ok := change.Evidence.(project.HistoryMigrationEvidence); ok {
		if change.Kind == project.ChangeProjectBaseline && migration.SchemaVersion == 1 {
			migration.SchemaVersion = project.ChangeSchemaVersion
		}
		return migrationLogRevision(change, migration.SchemaVersion, source)
	}
	transition, ok := change.Evidence.(project.HistoryTransitionEvidence)
	if !ok {
		return LogRevision{}, project.Internal("History revision evidence is invalid")
	}
	after := transition.After
	if after.Project.ID != change.ProjectID || after.Project.Revision != change.Revision ||
		after.Project.Generation != change.Generation {
		return LogRevision{}, project.Internal("History after-image identity is inconsistent")
	}
	var before project.ProjectDetail
	if transition.Before != nil {
		before = *transition.Before
		if before.Project.ID != change.ProjectID || before.Project.Revision != change.Revision-1 {
			return LogRevision{}, project.Internal("History before-image identity is inconsistent")
		}
	}
	group := LogRevision{
		Revision: change.Revision, Generation: change.Generation, ChangeKind: change.Kind,
		OccurredAt: change.OccurredAt, Source: source, Events: []LogEvent{},
	}
	if source.Kind == LogSourceMigration {
		return LogRevision{}, project.Internal("Current business history has a migration source")
	}
	switch change.Kind {
	case project.ChangeProjectCreated:
		if err := requireChangeEntity(change, "project", change.ProjectID); err != nil {
			return LogRevision{}, err
		}
		if group.Source.Kind != LogSourceOperator || transition.Before != nil {
			return LogRevision{}, project.Internal("Project creation history is inconsistent")
		}
		group.Events, err = projectCreatedEvents(change, after)
		if err != nil {
			return LogRevision{}, err
		}
	case project.ChangeProjectRenamed:
		if err := requireOperatorProjectChange(group, change, transition, before, after); err != nil {
			return LogRevision{}, err
		}
		if before.Project.Title == after.Project.Title {
			return LogRevision{}, project.Internal("Project rename history did not change the title")
		}
		group.Events = []LogEvent{ProjectRenamedEvent{
			LogEventBase: eventBase(change.ProjectID, change.Revision, LogEventProjectRenamed, change.ProjectID, nil),
			ProjectID:    change.ProjectID, PreviousTitle: before.Project.Title, Title: after.Project.Title,
		}}
	case project.ChangeProjectPaused:
		if err := requireOperatorProjectChange(group, change, transition, before, after); err != nil {
			return LogRevision{}, err
		}
		if before.Project.Status != project.ProjectStatusActive || after.Project.Status != project.ProjectStatusPaused {
			return LogRevision{}, project.Internal("Project pause history has invalid lifecycle states")
		}
		group.Events = []LogEvent{ProjectPausedEvent{
			LogEventBase: eventBase(change.ProjectID, change.Revision, LogEventProjectPaused, change.ProjectID, nil),
			ProjectID:    change.ProjectID, FromStatus: before.Project.Status, ToStatus: after.Project.Status,
		}}
	case project.ChangeProjectResumed:
		if err := requireOperatorProjectChange(group, change, transition, before, after); err != nil {
			return LogRevision{}, err
		}
		if before.Project.Status != project.ProjectStatusPaused || after.Project.Status != project.ProjectStatusActive {
			return LogRevision{}, project.Internal("Project resume history has invalid lifecycle states")
		}
		group.Events = []LogEvent{ProjectResumedEvent{
			LogEventBase: eventBase(change.ProjectID, change.Revision, LogEventProjectResumed, change.ProjectID, nil),
			ProjectID:    change.ProjectID, FromStatus: before.Project.Status, ToStatus: after.Project.Status,
		}}
	case project.ChangeProjectReopened:
		if err := requireChangeEntity(change, "project", change.ProjectID); err != nil || group.Source.Kind != LogSourceOperator {
			return LogRevision{}, project.Internal("Project reopen history has invalid source or identity")
		}
		result, ok := transition.Result.(project.HistoryReopenedProjectResult)
		if !ok {
			return LogRevision{}, project.Internal("Project reopen history result is invalid")
		}
		if before.Project.Status != project.ProjectStatusCompleted || after.Project.Status != project.ProjectStatusActive ||
			before.Project.Generation != result.PreviousGeneration || after.Project.Generation != result.PreviousGeneration+1 {
			return LogRevision{}, project.Internal("Project reopen history has invalid lifecycle states")
		}
		hint, found := findHint(after.Hints, result.HintID)
		if !found || hint.Generation != after.Project.Generation {
			return LogRevision{}, project.Internal("Project reopen history is missing its feedback Hint")
		}
		group.Events = []LogEvent{
			ProjectReopenedEvent{
				LogEventBase: eventBase(change.ProjectID, change.Revision, LogEventProjectReopened, change.ProjectID, nil),
				ProjectID:    change.ProjectID, PreviousGeneration: result.PreviousGeneration,
				CurrentGeneration: after.Project.Generation, FeedbackHintID: hint.ID,
			},
			hintCreatedEvent(change.ProjectID, change.Revision, hint, nil),
		}
	case project.ChangeHintCreated:
		if group.Source.Kind != LogSourceOperator || change.Entity.Type != "hint" || change.Entity.ID == nil {
			return LogRevision{}, project.Internal("Hint creation history has invalid source or identity")
		}
		result, ok := transition.Result.(project.HistoryAffectedRunsResult)
		if !ok {
			return LogRevision{}, project.Internal("Hint creation history result is invalid")
		}
		hint, found := findHint(after.Hints, *change.Entity.ID)
		if !found {
			return LogRevision{}, project.Internal("Hint creation history is missing its Hint")
		}
		group.Events = []LogEvent{hintCreatedEvent(change.ProjectID, change.Revision, hint, &result.AffectedRunCount)}
	case project.ChangeHintWithdrawn:
		if group.Source.Kind != LogSourceOperator || change.Entity.Type != "hint" || change.Entity.ID == nil {
			return LogRevision{}, project.Internal("Hint withdrawal history has invalid source or identity")
		}
		result, ok := transition.Result.(project.HistoryReasonResult)
		if !ok {
			return LogRevision{}, project.Internal("Hint withdrawal history result is invalid")
		}
		hint, found := findHint(after.Hints, *change.Entity.ID)
		if !found || hint.WithdrawnBy == nil || hint.WithdrawalReason == nil ||
			*hint.WithdrawalReason != result.Reason || *hint.WithdrawnBy != group.Source.Name {
			return LogRevision{}, project.Internal("Hint withdrawal history is missing its withdrawn Hint")
		}
		group.Events = []LogEvent{HintWithdrawnEvent{
			LogEventBase: eventBase(change.ProjectID, change.Revision, LogEventHintWithdrawn, hint.ID, nil),
			HintID:       hint.ID, Content: hint.Content, Generation: hint.Generation,
			WithdrawnBy: *hint.WithdrawnBy, Reason: result.Reason, AffectedRunCount: &result.AffectedRunCount,
		}}
	case project.ChangeIntentCreated:
		if change.Entity.Type != "intent" || change.Entity.ID == nil {
			return LogRevision{}, project.Internal("Intent creation history has invalid identity")
		}
		intent, found := findIntent(after.Intents, *change.Entity.ID)
		if !found {
			return LogRevision{}, project.Internal("Intent creation history is missing its Intent")
		}
		group.Events = []LogEvent{intentCreatedEvent(change.ProjectID, change.Revision, intent)}
	case project.ChangeIntentsProposed:
		if group.Source.Kind != LogSourceRun || change.Entity.Type != "intent_batch" || change.Entity.ID != nil {
			return LogRevision{}, project.Internal("Proposed Intents history has invalid source or identity")
		}
		result, ok := transition.Result.(project.HistoryProposedIntentsResult)
		if !ok {
			return LogRevision{}, project.Internal("Proposed Intents history result is invalid")
		}
		group.Events = make([]LogEvent, 0, len(result.IntentIDs))
		seen := make(map[string]struct{}, len(result.IntentIDs))
		for _, intentID := range result.IntentIDs {
			if intentID == "" {
				return LogRevision{}, project.Internal("Proposed Intents history contains an empty Intent ID")
			}
			if _, duplicate := seen[intentID]; duplicate {
				return LogRevision{}, project.Internal("Proposed Intents history contains duplicate Intent IDs")
			}
			seen[intentID] = struct{}{}
			intent, found := findIntent(after.Intents, intentID)
			if !found {
				return LogRevision{}, project.Internal("Proposed Intents history is missing an Intent")
			}
			group.Events = append(group.Events, intentCreatedEvent(change.ProjectID, change.Revision, intent))
		}
	case project.ChangeIntentConcluded:
		if group.Source.Kind != LogSourceRun || change.Entity.Type != "intent" || change.Entity.ID == nil {
			return LogRevision{}, project.Internal("Intent conclusion history has invalid source or identity")
		}
		result, ok := transition.Result.(project.HistoryConcludedIntentResult)
		if !ok {
			return LogRevision{}, project.Internal("Intent conclusion history result is invalid")
		}
		event, err := intentConcludedEvent(change.ProjectID, change.Revision, after, *change.Entity.ID, result.FactID)
		if err != nil {
			return LogRevision{}, err
		}
		group.Events = []LogEvent{event}
	case project.ChangeIntentCancelled:
		if group.Source.Kind != LogSourceOperator || change.Entity.Type != "intent" || change.Entity.ID == nil {
			return LogRevision{}, project.Internal("Intent cancellation history has invalid source or identity")
		}
		result, ok := transition.Result.(project.HistoryReasonResult)
		if !ok {
			return LogRevision{}, project.Internal("Intent cancellation history result is invalid")
		}
		intent, found := findIntent(after.Intents, *change.Entity.ID)
		if !found || intent.CancelledBy == nil || intent.CancellationReason == nil ||
			*intent.CancelledBy != group.Source.Name || *intent.CancellationReason != result.Reason {
			return LogRevision{}, project.Internal("Intent cancellation history is missing its cancelled Intent")
		}
		group.Events = []LogEvent{IntentCancelledEvent{
			LogEventBase: eventBase(change.ProjectID, change.Revision, LogEventIntentCancelled, intent.ID,
				&LogFocusTarget{Type: LogFocusIntent, ID: intent.ID}),
			IntentID: intent.ID, IntentKind: intent.Kind, Description: intent.Description,
			SourceFactIDs: append([]string(nil), intent.From...), CancelledBy: *intent.CancelledBy,
			Reason: result.Reason, AffectedRunCount: &result.AffectedRunCount,
		}}
	case project.ChangeFactRetracted:
		if group.Source.Kind != LogSourceOperator || change.Entity.Type != "fact" || change.Entity.ID == nil {
			return LogRevision{}, project.Internal("Fact retraction history has invalid source or identity")
		}
		result, ok := transition.Result.(project.HistoryReasonResult)
		if !ok {
			return LogRevision{}, project.Internal("Fact retraction history result is invalid")
		}
		fact, found := findFact(after.Facts, *change.Entity.ID)
		if !found || fact.RetractedBy == nil || fact.RetractionReason == nil ||
			*fact.RetractedBy != group.Source.Name || *fact.RetractionReason != result.Reason {
			return LogRevision{}, project.Internal("Fact retraction history is missing its retracted Fact")
		}
		group.Events = []LogEvent{FactRetractedEvent{
			LogEventBase: eventBase(change.ProjectID, change.Revision, LogEventFactRetracted, fact.ID,
				&LogFocusTarget{Type: LogFocusFact, ID: fact.ID}),
			FactID: fact.ID, Description: fact.Description, RetractedBy: *fact.RetractedBy,
			Reason: result.Reason, ReplacementFactID: fact.ReplacementFactID, AffectedRunCount: &result.AffectedRunCount,
		}}
	case project.ChangeFactSuperseded:
		if group.Source.Kind != LogSourceOperator || change.Entity.Type != "fact" || change.Entity.ID == nil {
			return LogRevision{}, project.Internal("Fact supersession history has invalid source or identity")
		}
		result, ok := transition.Result.(project.HistorySupersedeResult)
		if !ok {
			return LogRevision{}, project.Internal("Fact supersession history result is invalid")
		}
		original, originalFound := findFact(after.Facts, *change.Entity.ID)
		replacement, replacementFound := findFact(after.Facts, result.ReplacementFactID)
		if !originalFound || !replacementFound || original.RetractedBy == nil || original.RetractionReason == nil ||
			original.ReplacementFactID == nil || *original.ReplacementFactID != replacement.ID ||
			replacement.Supersedes == nil || *replacement.Supersedes != original.ID ||
			*original.RetractedBy != group.Source.Name || *original.RetractionReason != result.Reason {
			return LogRevision{}, project.Internal("Fact supersession history is missing its Fact lineage")
		}
		group.Events = []LogEvent{FactSupersededEvent{
			LogEventBase: eventBase(change.ProjectID, change.Revision, LogEventFactSuperseded, replacement.ID,
				&LogFocusTarget{Type: LogFocusFact, ID: replacement.ID}),
			OriginalFact:    FactReference{ID: original.ID, Description: original.Description},
			ReplacementFact: FactReference{ID: replacement.ID, Description: replacement.Description},
			Actor:           *original.RetractedBy, Reason: result.Reason, AffectedRunCount: &result.AffectedRunCount,
		}}
	case project.ChangeProjectCompleted:
		if err := requireChangeEntity(change, "project", change.ProjectID); err != nil {
			return LogRevision{}, err
		}
		if group.Source.Kind != LogSourceOperator && group.Source.Kind != LogSourceRun {
			return LogRevision{}, project.Internal("Project completion history has invalid source")
		}
		result, ok := transition.Result.(project.HistoryCompletedProjectResult)
		if !ok {
			return LogRevision{}, project.Internal("Project completion history result is invalid")
		}
		group.Events, err = projectCompletedEvents(change, after, result.CompletionIntentID, result.AffectedRunCount)
		if err != nil {
			return LogRevision{}, err
		}
	case project.ChangeBootstrapCompleted:
		if err := requireChangeEntity(change, "project", change.ProjectID); err != nil || group.Source.Kind != LogSourceRun {
			return LogRevision{}, project.Internal("Bootstrap completion history has invalid source or identity")
		}
		result, ok := transition.Result.(project.HistoryBootstrapCompletedResult)
		if !ok {
			return LogRevision{}, project.Internal("Bootstrap completion history result is invalid")
		}
		concluded, err := intentConcludedEvent(change.ProjectID, change.Revision, after, result.TargetIntentID, result.FactID)
		if err != nil {
			return LogRevision{}, err
		}
		completionEvents, err := projectCompletedEvents(change, after, result.CompletionIntentID, nil)
		if err != nil {
			return LogRevision{}, err
		}
		group.Events = append([]LogEvent{concluded}, completionEvents...)
	default:
		return LogRevision{}, project.Conflict("History change kind is unsupported")
	}
	if err := validateLogEvents(group.Events); err != nil {
		return LogRevision{}, err
	}
	return group, nil
}

func migrationLogRevision(
	change project.HistoryRevision,
	schemaVersion int,
	source LogSource,
) (LogRevision, error) {
	if change.Kind == "" || change.Entity.Type == "" {
		return LogRevision{}, project.Internal("Migration history identity is incomplete")
	}
	event := MigrationEvent{
		LogEventBase:       eventBase(change.ProjectID, change.Revision, LogEventMigration, string(change.Kind), nil),
		OriginalChangeKind: change.Kind,
		SchemaVersion:      schemaVersion,
		EntityType:         change.Entity.Type,
		EntityID:           change.Entity.ID,
	}
	group := LogRevision{
		Revision: change.Revision, Generation: change.Generation, ChangeKind: change.Kind,
		OccurredAt: change.OccurredAt, Source: source, Events: []LogEvent{event},
	}
	if err := validateLogEvents(group.Events); err != nil {
		return LogRevision{}, err
	}
	return group, nil
}

func decodeLogSource(source project.ChangeSource) (LogSource, error) {
	result := LogSource{Name: source.Name, RunID: source.RunID}
	switch source.Kind {
	case string(LogSourceOperator):
		result.Kind = LogSourceOperator
		if source.RunID != nil {
			return LogSource{}, project.Internal("Operator history source contains a Run ID")
		}
	case string(LogSourceRun):
		result.Kind = LogSourceRun
		if source.RunID == nil || *source.RunID == "" {
			return LogSource{}, project.Internal("Run history source is missing its Run ID")
		}
	case string(LogSourceMigration):
		result.Kind = LogSourceMigration
		if source.RunID != nil {
			return LogSource{}, project.Internal("Migration history source contains a Run ID")
		}
	default:
		return LogSource{}, project.Internal("History source kind is invalid")
	}
	if source.Name == "" {
		return LogSource{}, project.Internal("History source name is missing")
	}
	return result, nil
}

func requireOperatorProjectChange(
	group LogRevision,
	change project.HistoryRevision,
	transition project.HistoryTransitionEvidence,
	before project.ProjectDetail,
	after project.ProjectDetail,
) error {
	if err := requireChangeEntity(change, "project", change.ProjectID); err != nil {
		return err
	}
	if group.Source.Kind != LogSourceOperator || transition.Before == nil ||
		before.Project.ID != after.Project.ID || before.Project.Generation != after.Project.Generation {
		return project.Internal("Project history has invalid source or identity")
	}
	return nil
}

func requireChangeEntity(change project.HistoryRevision, entityType, entityID string) error {
	if change.Entity.Type != entityType || change.Entity.ID == nil || *change.Entity.ID != entityID {
		return project.Internal("History change entity is inconsistent")
	}
	return nil
}

func projectCreatedEvents(change project.HistoryRevision, after project.ProjectDetail) ([]LogEvent, error) {
	origin, originFound := findFact(after.Facts, "origin")
	goal, goalFound := findFact(after.Facts, "goal")
	if !originFound || !goalFound {
		return nil, project.Internal("Project creation history is missing Origin or Goal")
	}
	events := []LogEvent{ProjectCreatedEvent{
		LogEventBase: eventBase(change.ProjectID, change.Revision, LogEventProjectCreated, change.ProjectID,
			&LogFocusTarget{Type: LogFocusFact, ID: origin.ID}),
		ProjectID: after.Project.ID,
		Title:     after.Project.Title,
		Origin:    FactReference{ID: origin.ID, Description: origin.Description},
		Goal:      FactReference{ID: goal.ID, Description: goal.Description},
	}}
	hints := append([]project.Hint(nil), after.Hints...)
	sort.Slice(hints, func(left, right int) bool { return hints[left].ID < hints[right].ID })
	for _, hint := range hints {
		events = append(events, hintCreatedEvent(change.ProjectID, change.Revision, hint, nil))
	}
	bootstrapIntents := make([]project.Intent, 0, 1)
	for _, intent := range after.Intents {
		if intent.Kind != project.IntentKindBootstrap {
			return nil, project.Internal("Project creation history contains an unexpected Intent")
		}
		bootstrapIntents = append(bootstrapIntents, intent)
	}
	sort.Slice(bootstrapIntents, func(left, right int) bool { return bootstrapIntents[left].ID < bootstrapIntents[right].ID })
	for _, intent := range bootstrapIntents {
		events = append(events, intentCreatedEvent(change.ProjectID, change.Revision, intent))
	}
	return events, nil
}

func hintCreatedEvent(projectID string, revision int64, hint project.Hint, affectedRunCount *int) HintCreatedEvent {
	return HintCreatedEvent{
		LogEventBase: eventBase(projectID, revision, LogEventHintCreated, hint.ID, nil),
		HintID:       hint.ID, Content: hint.Content, Creator: hint.Creator,
		Generation: hint.Generation, Status: hint.Status, AffectedRunCount: affectedRunCount,
	}
}

func intentCreatedEvent(projectID string, revision int64, intent project.Intent) IntentCreatedEvent {
	return IntentCreatedEvent{
		LogEventBase: eventBase(projectID, revision, LogEventIntentCreated, intent.ID,
			&LogFocusTarget{Type: LogFocusIntent, ID: intent.ID}),
		IntentID: intent.ID, IntentKind: intent.Kind, Status: intent.Status,
		Description: intent.Description, Creator: intent.Creator,
		SourceFactIDs: append([]string(nil), intent.From...), TargetFactID: intent.To,
	}
}

func intentConcludedEvent(
	projectID string,
	revision int64,
	after project.ProjectDetail,
	intentID string,
	factID string,
) (IntentConcludedEvent, error) {
	intent, intentFound := findIntent(after.Intents, intentID)
	fact, factFound := findFact(after.Facts, factID)
	if !intentFound || !factFound || intent.Status != project.IntentStatusConcluded || intent.To == nil || *intent.To != fact.ID {
		return IntentConcludedEvent{}, project.Internal("Intent conclusion history is missing its Intent or produced Fact")
	}
	return IntentConcludedEvent{
		LogEventBase: eventBase(projectID, revision, LogEventIntentConcluded, intent.ID,
			&LogFocusTarget{Type: LogFocusFact, ID: fact.ID}),
		IntentID: intent.ID, IntentKind: intent.Kind, Description: intent.Description,
		SourceFactIDs: append([]string(nil), intent.From...),
		ProducedFact:  FactReference{ID: fact.ID, Description: fact.Description},
	}, nil
}

func projectCompletedEvents(
	change project.HistoryRevision,
	after project.ProjectDetail,
	completionIntentID string,
	affectedRunCount *int,
) ([]LogEvent, error) {
	if after.Project.Status != project.ProjectStatusCompleted {
		return nil, project.Internal("Project completion history does not contain a completed Project")
	}
	intent, intentFound := findIntent(after.Intents, completionIntentID)
	completion, completionFound := findCompletion(after.CompletionRecords, completionIntentID)
	goal, goalFound := findFact(after.Facts, "goal")
	if !intentFound || !completionFound || !goalFound || intent.Kind != project.IntentKindCompletion ||
		intent.Status != project.IntentStatusConcluded || intent.To == nil || *intent.To != goal.ID ||
		completion.Generation != change.Generation || completion.Description != intent.Description {
		return nil, project.Internal("Project completion history is missing its Completion evidence")
	}
	return []LogEvent{
		intentCreatedEvent(change.ProjectID, change.Revision, intent),
		ProjectCompletedEvent{
			LogEventBase: eventBase(change.ProjectID, change.Revision, LogEventProjectCompleted, change.ProjectID,
				&LogFocusTarget{Type: LogFocusFact, ID: goal.ID}),
			ProjectID: change.ProjectID, CompletionIntentID: intent.ID, Description: completion.Description,
			SourceFactIDs: append([]string(nil), intent.From...), CompletionGeneration: completion.Generation,
			AffectedRunCount: affectedRunCount,
		},
	}, nil
}

func eventBase(projectID string, revision int64, kind LogEventKind, identity string, focus *LogFocusTarget) LogEventBase {
	return LogEventBase{
		EventID: fmt.Sprintf("%s:r%d:%s:%s", projectID, revision, kind, identity),
		Kind:    kind, FocusTarget: focus,
	}
}

func findFact(facts []project.Fact, id string) (project.Fact, bool) {
	for _, fact := range facts {
		if fact.ID == id {
			return fact, true
		}
	}
	return project.Fact{}, false
}

func findHint(hints []project.Hint, id string) (project.Hint, bool) {
	for _, hint := range hints {
		if hint.ID == id {
			return hint, true
		}
	}
	return project.Hint{}, false
}

func findIntent(intents []project.Intent, id string) (project.Intent, bool) {
	for _, intent := range intents {
		if intent.ID == id {
			return intent, true
		}
	}
	return project.Intent{}, false
}

func findCompletion(records []project.CompletionRecord, intentID string) (project.CompletionRecord, bool) {
	for _, record := range records {
		if record.IntentID == intentID {
			return record, true
		}
	}
	return project.CompletionRecord{}, false
}

func validateLogEvents(events []LogEvent) error {
	if len(events) == 0 {
		return project.Internal("History revision produced no Project Log events")
	}
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		if event == nil {
			return project.Internal("History revision produced an empty Project Log event")
		}
		base := event.logEvent()
		if base.EventID == "" || base.Kind == "" {
			return project.Internal("History revision produced an invalid Project Log event")
		}
		if _, duplicate := seen[base.EventID]; duplicate {
			return project.Internal("History revision produced duplicate Project Log event IDs")
		}
		if err := validateLogEventPayload(event); err != nil {
			return err
		}
		seen[base.EventID] = struct{}{}
	}
	return nil
}

func validateLogEventPayload(event LogEvent) error {
	switch event := event.(type) {
	case ProjectCreatedEvent:
		if event.Kind != LogEventProjectCreated || !hasLogText(event.ProjectID) || !hasLogText(event.Title) ||
			!validFactReference(event.Origin) || !validFactReference(event.Goal) || event.Origin.ID == event.Goal.ID ||
			!matchesLogFocus(event.FocusTarget, LogFocusFact, event.Origin.ID) {
			return project.Internal("Project creation produced incomplete Project Log evidence")
		}
	case ProjectRenamedEvent:
		if event.Kind != LogEventProjectRenamed || !hasLogText(event.ProjectID) ||
			!hasLogText(event.PreviousTitle) || !hasLogText(event.Title) || event.PreviousTitle == event.Title ||
			event.FocusTarget != nil {
			return project.Internal("Project rename produced incomplete Project Log evidence")
		}
	case ProjectPausedEvent:
		if event.Kind != LogEventProjectPaused || !hasLogText(event.ProjectID) ||
			event.FromStatus != project.ProjectStatusActive || event.ToStatus != project.ProjectStatusPaused ||
			event.FocusTarget != nil {
			return project.Internal("Project pause produced incomplete Project Log evidence")
		}
	case ProjectResumedEvent:
		if event.Kind != LogEventProjectResumed || !hasLogText(event.ProjectID) ||
			event.FromStatus != project.ProjectStatusPaused || event.ToStatus != project.ProjectStatusActive ||
			event.FocusTarget != nil {
			return project.Internal("Project resume produced incomplete Project Log evidence")
		}
	case ProjectReopenedEvent:
		if event.Kind != LogEventProjectReopened || !hasLogText(event.ProjectID) ||
			event.PreviousGeneration < 1 || event.CurrentGeneration != event.PreviousGeneration+1 ||
			!hasLogText(event.FeedbackHintID) || event.FocusTarget != nil {
			return project.Internal("Project reopen produced incomplete Project Log evidence")
		}
	case ProjectCompletedEvent:
		if event.Kind != LogEventProjectCompleted || !hasLogText(event.ProjectID) ||
			!hasLogText(event.CompletionIntentID) || !hasLogText(event.Description) ||
			!validRequiredIDs(event.SourceFactIDs) || event.CompletionGeneration < 1 ||
			!validAffectedRunCount(event.AffectedRunCount) || !validLogFocus(event.FocusTarget, LogFocusFact) {
			return project.Internal("Project completion produced incomplete Project Log evidence")
		}
	case HintCreatedEvent:
		if event.Kind != LogEventHintCreated || !hasLogText(event.HintID) || !hasLogText(event.Content) ||
			!hasLogText(event.Creator) || event.Generation < 1 || event.Status != "active" ||
			!validAffectedRunCount(event.AffectedRunCount) || event.FocusTarget != nil {
			return project.Internal("Hint creation produced incomplete Project Log evidence")
		}
	case HintWithdrawnEvent:
		if event.Kind != LogEventHintWithdrawn || !hasLogText(event.HintID) || !hasLogText(event.Content) ||
			event.Generation < 1 || !hasLogText(event.WithdrawnBy) || !hasLogText(event.Reason) ||
			!validAffectedRunCount(event.AffectedRunCount) || event.FocusTarget != nil {
			return project.Internal("Hint withdrawal produced incomplete Project Log evidence")
		}
	case IntentCreatedEvent:
		if event.Kind != LogEventIntentCreated || !hasLogText(event.IntentID) ||
			!validIntentKind(event.IntentKind) || !validIntentStatus(event.Status) ||
			!hasLogText(event.Description) || !hasLogText(event.Creator) ||
			!validRequiredIDs(event.SourceFactIDs) || !validIntentTarget(event.Status, event.TargetFactID) ||
			!matchesLogFocus(event.FocusTarget, LogFocusIntent, event.IntentID) {
			return project.Internal("Intent creation produced incomplete Project Log evidence")
		}
	case IntentConcludedEvent:
		if event.Kind != LogEventIntentConcluded || !hasLogText(event.IntentID) ||
			!validIntentKind(event.IntentKind) || !hasLogText(event.Description) ||
			!validRequiredIDs(event.SourceFactIDs) || !validFactReference(event.ProducedFact) ||
			!matchesLogFocus(event.FocusTarget, LogFocusFact, event.ProducedFact.ID) {
			return project.Internal("Intent conclusion produced incomplete Project Log evidence")
		}
	case IntentCancelledEvent:
		if event.Kind != LogEventIntentCancelled || !hasLogText(event.IntentID) ||
			!validIntentKind(event.IntentKind) || !hasLogText(event.Description) ||
			!validRequiredIDs(event.SourceFactIDs) || !hasLogText(event.CancelledBy) ||
			!hasLogText(event.Reason) || !validAffectedRunCount(event.AffectedRunCount) ||
			!matchesLogFocus(event.FocusTarget, LogFocusIntent, event.IntentID) {
			return project.Internal("Intent cancellation produced incomplete Project Log evidence")
		}
	case FactRetractedEvent:
		if event.Kind != LogEventFactRetracted || !hasLogText(event.FactID) ||
			!hasLogText(event.Description) || !hasLogText(event.RetractedBy) || !hasLogText(event.Reason) ||
			!validOptionalID(event.ReplacementFactID) || !validAffectedRunCount(event.AffectedRunCount) ||
			!matchesLogFocus(event.FocusTarget, LogFocusFact, event.FactID) {
			return project.Internal("Fact retraction produced incomplete Project Log evidence")
		}
	case FactSupersededEvent:
		if event.Kind != LogEventFactSuperseded || !validFactReference(event.OriginalFact) ||
			!validFactReference(event.ReplacementFact) || event.OriginalFact.ID == event.ReplacementFact.ID ||
			!hasLogText(event.Actor) || !hasLogText(event.Reason) ||
			!validAffectedRunCount(event.AffectedRunCount) ||
			!matchesLogFocus(event.FocusTarget, LogFocusFact, event.ReplacementFact.ID) {
			return project.Internal("Fact supersession produced incomplete Project Log evidence")
		}
	case MigrationEvent:
		if event.Kind != LogEventMigration || event.OriginalChangeKind == "" ||
			event.SchemaVersion < 0 || event.SchemaVersion > project.ChangeSchemaVersion ||
			!hasLogText(event.EntityType) || !validOptionalID(event.EntityID) || event.FocusTarget != nil {
			return project.Internal("Migration produced incomplete Project Log evidence")
		}
	default:
		return project.Internal("History revision produced an unsupported Project Log event")
	}
	return nil
}

func validAffectedRunCount(count *int) bool {
	return count == nil || *count >= 0
}

func validOptionalID(id *string) bool {
	return id == nil || hasLogText(*id)
}

func validLogFocus(focus *LogFocusTarget, focusType LogFocusType) bool {
	return focus != nil && focus.Type == focusType && hasLogText(focus.ID)
}

func matchesLogFocus(focus *LogFocusTarget, focusType LogFocusType, id string) bool {
	return validLogFocus(focus, focusType) && focus.ID == id
}

func validIntentKind(kind project.IntentKind) bool {
	return kind == project.IntentKindBootstrap || kind == project.IntentKindExplore || kind == project.IntentKindCompletion
}

func validIntentStatus(status project.IntentStatus) bool {
	return status == project.IntentStatusOpen || status == project.IntentStatusConcluded || status == project.IntentStatusCancelled
}

func validIntentTarget(status project.IntentStatus, target *string) bool {
	if status == project.IntentStatusConcluded {
		return target != nil && hasLogText(*target)
	}
	return target == nil
}

func validRequiredIDs(ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !hasLogText(id) {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func validFactReference(fact FactReference) bool {
	return hasLogText(fact.ID) && hasLogText(fact.Description)
}

func hasLogText(value string) bool {
	return strings.TrimSpace(value) != ""
}
