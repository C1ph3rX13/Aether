package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// LegacyHistorySchema identifies rows written before the delta/snapshot
// history format. Their payload is retained as evidence, but it is never
// interpreted as a new causal event.
const LegacyHistorySchema = "legacy-snapshot/v1"

// DeltaHistorySchema identifies the typed, replayable history payload.
const DeltaHistorySchema = "delta/v1"

// ProjectSnapshotSchema identifies normalized full Project state snapshots.
const ProjectSnapshotSchema = "project-snapshot/v1"

// ChangeSchemaVersion is the version of the current history envelope. Legacy
// snapshot rows use schema versions 0 and 1 inside their retained data field.
const ChangeSchemaVersion = 2

// HistoryDelta is deliberately a typed set of entity-level changes. It does
// not contain a complete before/after Project Detail, so its write size is
// bounded by the entities changed by one command.
type HistoryDelta struct {
	SchemaVersion int                                   `json:"schema_version"`
	Kind          ChangeKind                            `json:"kind"`
	Project       *HistoryValueDelta[ProjectMeta]       `json:"project,omitempty"`
	Facts         []HistoryValueDelta[Fact]             `json:"facts,omitempty"`
	Intents       []HistoryValueDelta[Intent]           `json:"intents,omitempty"`
	Hints         []HistoryValueDelta[Hint]             `json:"hints,omitempty"`
	Completions   []HistoryValueDelta[CompletionRecord] `json:"completions,omitempty"`
	Result        map[string]any                        `json:"result,omitempty"`
}

// HistoryValueDelta contains the old and new value for one entity identity.
// A nil Before denotes creation and a nil After denotes removal.
type HistoryValueDelta[T any] struct {
	ID string `json:"id"`

	// Before and After are complete typed values while a command is building a
	// delta. The JSON representation below stores only the fields that changed;
	// the reducer merges those patches into its current typed state.
	Before       *T             `json:"-"`
	After        *T             `json:"-"`
	BeforeFields map[string]any `json:"before,omitempty"`
	AfterFields  map[string]any `json:"after,omitempty"`
}

type historyEnvelope struct {
	SchemaVersion int          `json:"schema_version"`
	Source        ChangeSource `json:"source"`
	Delta         HistoryDelta `json:"delta"`
}

// buildHistoryDelta derives a compact typed delta from the command's two
// in-transaction projections. The projections are only used to identify the
// changed entities; the persisted payload contains no full Project images.
func buildHistoryDelta(
	kind ChangeKind,
	before, after ProjectDetail,
	result map[string]any,
) (HistoryDelta, error) {
	if !validHistoryKind(kind) {
		return HistoryDelta{}, fmt.Errorf("unsupported history change kind %q", kind)
	}
	delta := HistoryDelta{SchemaVersion: ChangeSchemaVersion, Kind: kind, Result: result}
	if before.Project.ID != after.Project.ID || !reflect.DeepEqual(before.Project, after.Project) {
		var beforeValue, afterValue *ProjectMeta
		if before.Project.ID != "" {
			value := before.Project
			beforeValue = &value
		}
		if after.Project.ID != "" {
			value := after.Project
			afterValue = &value
		}
		delta.Project = &HistoryValueDelta[ProjectMeta]{ID: after.Project.ID, Before: beforeValue, After: afterValue}
	}
	delta.Facts = historyValueDeltaSet(before.Facts, after.Facts, func(value Fact) string { return value.ID })
	delta.Intents = historyValueDeltaSet(before.Intents, after.Intents, func(value Intent) string { return value.ID })
	delta.Hints = historyValueDeltaSet(before.Hints, after.Hints, func(value Hint) string { return value.ID })
	delta.Completions = historyValueDeltaSet(before.CompletionRecords, after.CompletionRecords, func(value CompletionRecord) string {
		return value.IntentID
	})
	if err := compactHistoryDelta(&delta); err != nil {
		return HistoryDelta{}, err
	}
	return delta, nil
}

func compactHistoryDelta(delta *HistoryDelta) error {
	if delta.Project != nil {
		if err := compactHistoryValueDelta(delta.Project); err != nil {
			return err
		}
	}
	for index := range delta.Facts {
		if err := compactHistoryValueDelta(&delta.Facts[index]); err != nil {
			return err
		}
	}
	for index := range delta.Intents {
		if err := compactHistoryValueDelta(&delta.Intents[index]); err != nil {
			return err
		}
	}
	for index := range delta.Hints {
		if err := compactHistoryValueDelta(&delta.Hints[index]); err != nil {
			return err
		}
	}
	for index := range delta.Completions {
		if err := compactHistoryValueDelta(&delta.Completions[index]); err != nil {
			return err
		}
	}
	return nil
}

func compactHistoryValueDelta[T any](delta *HistoryValueDelta[T]) error {
	before, err := historyValueFields(delta.Before)
	if err != nil {
		return err
	}
	after, err := historyValueFields(delta.After)
	if err != nil {
		return err
	}
	switch {
	case before == nil:
		delta.BeforeFields = nil
	case after == nil:
		delta.BeforeFields = before
	default:
		delta.BeforeFields = changedHistoryFields(before, after)
	}
	switch {
	case after == nil:
		delta.AfterFields = nil
	case before == nil:
		delta.AfterFields = after
	default:
		delta.AfterFields = changedHistoryFields(after, before)
	}
	return nil
}

func historyValueFields[T any](value *T) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func changedHistoryFields(value, other map[string]any) map[string]any {
	changed := make(map[string]any)
	for key, current := range value {
		if previous, exists := other[key]; !exists || !reflect.DeepEqual(current, previous) {
			changed[key] = current
		}
	}
	// A JSON field tagged with omitempty disappears when a pointer or slice is
	// cleared. Preserve that deletion explicitly so the reducer does not leave
	// the predecessor value behind when merging the sparse successor patch.
	for key := range other {
		if _, exists := value[key]; !exists {
			changed[key] = nil
		}
	}
	return changed
}

func historyValueDeltaSet[T any](before, after []T, key func(T) string) []HistoryValueDelta[T] {
	beforeByID := make(map[string]T, len(before))
	afterByID := make(map[string]T, len(after))
	ids := make(map[string]struct{}, len(before)+len(after))
	for _, value := range before {
		id := key(value)
		beforeByID[id] = value
		ids[id] = struct{}{}
	}
	for _, value := range after {
		id := key(value)
		afterByID[id] = value
		ids[id] = struct{}{}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	result := make([]HistoryValueDelta[T], 0, len(ordered))
	for _, id := range ordered {
		beforeValue, hasBefore := beforeByID[id]
		afterValue, hasAfter := afterByID[id]
		if hasBefore && hasAfter && reflect.DeepEqual(beforeValue, afterValue) {
			continue
		}
		entry := HistoryValueDelta[T]{ID: id}
		if hasBefore {
			value := beforeValue
			entry.Before = &value
		}
		if hasAfter {
			value := afterValue
			entry.After = &value
		}
		result = append(result, entry)
	}
	return result
}

func marshalHistoryDelta(delta HistoryDelta) (json.RawMessage, error) {
	if delta.SchemaVersion != ChangeSchemaVersion {
		return nil, fmt.Errorf("history delta schema %d is unsupported", delta.SchemaVersion)
	}
	data, err := json.Marshal(delta)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func marshalHistoryEnvelope(source ChangeSource, delta HistoryDelta) (json.RawMessage, error) {
	data, err := json.Marshal(historyEnvelope{
		SchemaVersion: ChangeSchemaVersion,
		Source:        source,
		Delta:         delta,
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

func decodeHistoryDelta(data []byte) (HistoryDelta, error) {
	var delta HistoryDelta
	if err := json.Unmarshal(data, &delta); err != nil {
		return HistoryDelta{}, err
	}
	if delta.SchemaVersion != ChangeSchemaVersion {
		return HistoryDelta{}, newHistoryEvidenceConflict("history delta schema %d is unsupported", delta.SchemaVersion)
	}
	if !validHistoryKind(delta.Kind) {
		return HistoryDelta{}, newHistoryEvidenceConflict("history delta kind %q is unsupported", delta.Kind)
	}
	if err := validateHistoryDeltaShape(delta); err != nil {
		return HistoryDelta{}, err
	}
	return delta, nil
}

func validateHistoryDeltaShape(delta HistoryDelta) error {
	if delta.Project == nil {
		return fmt.Errorf("history delta for %s is missing the Project transition", delta.Kind)
	}
	allowFacts, allowIntents, allowHints, allowCompletions := false, false, false, false
	switch delta.Kind {
	case ChangeProjectCreated:
		allowFacts, allowIntents, allowHints, allowCompletions = true, true, true, true
	case ChangeIntentCreated, ChangeIntentsProposed:
		allowIntents = true
	case ChangeIntentConcluded:
		allowFacts, allowIntents = true, true
	case ChangeIntentCancelled:
		allowIntents = true
	case ChangeHintCreated, ChangeHintWithdrawn, ChangeProjectReopened:
		allowHints = true
	case ChangeFactRetracted, ChangeFactSuperseded:
		allowFacts = true
	case ChangeProjectCompleted:
		allowIntents, allowCompletions = true, true
	case ChangeBootstrapCompleted:
		allowFacts, allowIntents, allowCompletions = true, true, true
	case ChangeProjectRenamed, ChangeProjectPaused, ChangeProjectResumed:
	default:
		return fmt.Errorf("history delta kind %q is unsupported", delta.Kind)
	}
	if !allowFacts && len(delta.Facts) > 0 {
		return fmt.Errorf("history delta kind %s cannot contain Fact changes", delta.Kind)
	}
	if !allowIntents && len(delta.Intents) > 0 {
		return fmt.Errorf("history delta kind %s cannot contain Intent changes", delta.Kind)
	}
	if !allowHints && len(delta.Hints) > 0 {
		return fmt.Errorf("history delta kind %s cannot contain Hint changes", delta.Kind)
	}
	if !allowCompletions && len(delta.Completions) > 0 {
		return fmt.Errorf("history delta kind %s cannot contain completion changes", delta.Kind)
	}
	allowedResults := map[ChangeKind]map[string]struct{}{
		ChangeIntentConcluded:    {"fact_id": {}},
		ChangeIntentCancelled:    {"reason": {}, "affected_run_count": {}},
		ChangeHintCreated:        {"affected_run_count": {}},
		ChangeHintWithdrawn:      {"reason": {}, "affected_run_count": {}},
		ChangeFactRetracted:      {"reason": {}, "affected_run_count": {}},
		ChangeFactSuperseded:     {"reason": {}, "replacement_fact_id": {}, "affected_run_count": {}},
		ChangeProjectCompleted:   {"completion_intent_id": {}, "affected_run_count": {}},
		ChangeProjectReopened:    {"hint_id": {}, "previous_generation": {}},
		ChangeIntentsProposed:    {"intent_ids": {}},
		ChangeBootstrapCompleted: {"fact_id": {}, "target_intent_id": {}, "completion_intent_id": {}},
	}
	if allowed, known := allowedResults[delta.Kind]; known {
		for key := range delta.Result {
			if _, ok := allowed[key]; !ok {
				return fmt.Errorf("history delta kind %s has an unsupported result field %q", delta.Kind, key)
			}
		}
	} else if len(delta.Result) > 0 {
		return fmt.Errorf("history delta kind %s cannot contain result fields", delta.Kind)
	}
	return nil
}

func decodeHistoryEnvelope(data []byte) (historyEnvelope, error) {
	var envelope historyEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return historyEnvelope{}, err
	}
	if envelope.SchemaVersion != ChangeSchemaVersion {
		return historyEnvelope{}, fmt.Errorf("history envelope schema %d is unsupported", envelope.SchemaVersion)
	}
	if _, err := decodeHistoryDelta(mustMarshal(envelope.Delta)); err != nil {
		return historyEnvelope{}, err
	}
	return envelope, nil
}

func mustMarshal(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Errorf("marshal canonical history value: %w", err))
	}
	return data
}

// normalizeProjectDetail is the canonical state representation used for
// snapshots and replay comparisons. Database query order is not part of the
// history contract, so arrays and Intent source IDs are sorted explicitly.
func normalizeProjectDetail(detail ProjectDetail) ProjectDetail {
	detail.Facts = append([]Fact(nil), detail.Facts...)
	detail.Intents = append([]Intent(nil), detail.Intents...)
	detail.Hints = append([]Hint(nil), detail.Hints...)
	detail.CompletionRecords = append([]CompletionRecord(nil), detail.CompletionRecords...)
	sort.Slice(detail.Facts, func(left, right int) bool { return detail.Facts[left].ID < detail.Facts[right].ID })
	sort.Slice(detail.Intents, func(left, right int) bool {
		if detail.Intents[left].CreatedAt != detail.Intents[right].CreatedAt {
			return detail.Intents[left].CreatedAt < detail.Intents[right].CreatedAt
		}
		return detail.Intents[left].ID < detail.Intents[right].ID
	})
	for index := range detail.Intents {
		detail.Intents[index].From = append([]string(nil), detail.Intents[index].From...)
		sort.Strings(detail.Intents[index].From)
	}
	sort.Slice(detail.Hints, func(left, right int) bool {
		if detail.Hints[left].CreatedAt != detail.Hints[right].CreatedAt {
			return detail.Hints[left].CreatedAt < detail.Hints[right].CreatedAt
		}
		return detail.Hints[left].ID < detail.Hints[right].ID
	})
	sort.Slice(detail.CompletionRecords, func(left, right int) bool {
		if detail.CompletionRecords[left].Generation != detail.CompletionRecords[right].Generation {
			return detail.CompletionRecords[left].Generation < detail.CompletionRecords[right].Generation
		}
		return detail.CompletionRecords[left].IntentID < detail.CompletionRecords[right].IntentID
	})
	if detail.Facts == nil {
		detail.Facts = []Fact{}
	}
	if detail.Intents == nil {
		detail.Intents = []Intent{}
	}
	if detail.Hints == nil {
		detail.Hints = []Hint{}
	}
	if detail.CompletionRecords == nil {
		detail.CompletionRecords = []CompletionRecord{}
	}
	return detail
}

func marshalCanonicalProjectDetail(detail ProjectDetail) ([]byte, error) {
	return json.Marshal(normalizeProjectDetail(detail))
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func canonicalJSON(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func applyHistoryDelta(state *ProjectDetail, delta HistoryDelta, projectID string, revision int64, generation int) error {
	if state == nil {
		return fmt.Errorf("history reducer received nil state")
	}
	if delta.SchemaVersion != ChangeSchemaVersion || !validHistoryKind(delta.Kind) {
		return fmt.Errorf("history delta schema or kind is unsupported")
	}
	if delta.Project != nil {
		if err := applyProjectValueDelta(&state.Project, *delta.Project, projectID, revision); err != nil {
			return err
		}
	}
	var err error
	state.Facts, err = applyEntityValueDeltas(state.Facts, delta.Facts, func(value Fact) string { return value.ID })
	if err != nil {
		return fmt.Errorf("facts delta: %w", err)
	}
	state.Intents, err = applyEntityValueDeltas(state.Intents, delta.Intents, func(value Intent) string { return value.ID })
	if err != nil {
		return fmt.Errorf("intents delta: %w", err)
	}
	state.Hints, err = applyEntityValueDeltas(state.Hints, delta.Hints, func(value Hint) string { return value.ID })
	if err != nil {
		return fmt.Errorf("hints delta: %w", err)
	}
	state.CompletionRecords, err = applyEntityValueDeltas(state.CompletionRecords, delta.Completions, func(value CompletionRecord) string {
		return value.IntentID
	})
	if err != nil {
		return fmt.Errorf("completion delta: %w", err)
	}
	*state = normalizeProjectDetail(*state)
	if state.Project.ID != "" && state.Project.ID != projectID {
		return fmt.Errorf("history reducer project identity mismatch")
	}
	if state.Project.ID != "" && (state.Project.Revision != revision || state.Project.Generation != generation) {
		return fmt.Errorf("history reducer revision identity mismatch")
	}
	return nil
}

// reverseHistoryDelta reconstructs the predecessor image from a complete
// successor. It is used only when a pre-delta Project reaches its first new
// command before the application-side cutover backfill has run.
func reverseHistoryDelta(state ProjectDetail, delta HistoryDelta, projectID string, revision int64, generation int) (ProjectDetail, error) {
	state = normalizeProjectDetail(state)
	if state.Project.ID != projectID {
		return ProjectDetail{}, fmt.Errorf("history predecessor Project mismatch")
	}
	if delta.Project != nil {
		afterFields, err := historyDeltaFields(delta.Project.After, delta.Project.AfterFields)
		if err != nil || afterFields == nil || !historyFieldsMatch(state.Project, afterFields) {
			return ProjectDetail{}, fmt.Errorf("history predecessor Project successor mismatch")
		}
		beforeFields, err := historyDeltaFields(delta.Project.Before, delta.Project.BeforeFields)
		if err != nil {
			return ProjectDetail{}, err
		}
		if beforeFields == nil {
			state.Project = ProjectMeta{}
		} else {
			state.Project, err = mergeHistoryFields(state.Project, beforeFields)
			if err != nil {
				return ProjectDetail{}, err
			}
		}
	}
	var err error
	state.Facts, err = reverseHistoryValues(state.Facts, delta.Facts, func(value Fact) string { return value.ID })
	if err != nil {
		return ProjectDetail{}, err
	}
	state.Intents, err = reverseHistoryValues(state.Intents, delta.Intents, func(value Intent) string { return value.ID })
	if err != nil {
		return ProjectDetail{}, err
	}
	state.Hints, err = reverseHistoryValues(state.Hints, delta.Hints, func(value Hint) string { return value.ID })
	if err != nil {
		return ProjectDetail{}, err
	}
	state.CompletionRecords, err = reverseHistoryValues(state.CompletionRecords, delta.Completions, func(value CompletionRecord) string { return value.IntentID })
	if err != nil {
		return ProjectDetail{}, err
	}
	state = normalizeProjectDetail(state)
	if state.Project.ID != "" {
		if state.Project.Revision != revision {
			state.Project.Revision = revision - 1
		}
		if state.Project.Generation > generation && generation > 0 {
			state.Project.Generation = generation
		}
	}
	return state, nil
}

func reverseHistoryValues[T any](current []T, changes []HistoryValueDelta[T], key func(T) string) ([]T, error) {
	byID := make(map[string]T, len(current))
	for _, value := range current {
		byID[key(value)] = value
	}
	for _, change := range changes {
		value, exists := byID[change.ID]
		afterFields, err := historyDeltaFields(change.After, change.AfterFields)
		if err != nil {
			return nil, err
		}
		if afterFields != nil {
			if !exists || !historyFieldsMatch(value, afterFields) {
				return nil, fmt.Errorf("history predecessor successor mismatch for %s", change.ID)
			}
		} else if exists {
			return nil, fmt.Errorf("history predecessor deletion mismatch for %s", change.ID)
		}
		beforeFields, err := historyDeltaFields(change.Before, change.BeforeFields)
		if err != nil {
			return nil, err
		}
		if beforeFields == nil {
			delete(byID, change.ID)
		} else {
			if exists {
				previous, err := mergeHistoryFields(value, beforeFields)
				if err != nil {
					return nil, err
				}
				byID[change.ID] = previous
			} else {
				previous, err := historyValueFromFields[T](beforeFields)
				if err != nil {
					return nil, err
				}
				byID[change.ID] = previous
			}
		}
	}
	values := make([]T, 0, len(byID))
	for _, value := range byID {
		values = append(values, value)
	}
	return values, nil
}

func applyProjectValueDelta(state *ProjectMeta, delta HistoryValueDelta[ProjectMeta], projectID string, revision int64) error {
	if delta.ID != "" && delta.ID != projectID {
		return fmt.Errorf("project delta identity mismatch")
	}
	beforeFields, err := historyDeltaFields(delta.Before, delta.BeforeFields)
	if err != nil {
		return err
	}
	if beforeFields != nil {
		if !historyFieldsMatch(*state, beforeFields) {
			return fmt.Errorf("project delta predecessor mismatch")
		}
	} else if state.ID != "" {
		return fmt.Errorf("project delta expected an empty predecessor")
	}
	afterFields, err := historyDeltaFields(delta.After, delta.AfterFields)
	if err != nil {
		return err
	}
	if afterFields == nil {
		return fmt.Errorf("project delta cannot remove the Project")
	}
	merged, err := mergeHistoryFields(*state, afterFields)
	if err != nil {
		return err
	}
	if merged.ID != projectID || merged.Revision != revision {
		return fmt.Errorf("project delta successor identity mismatch")
	}
	*state = merged
	return nil
}

func applyEntityValueDeltas[T any](state []T, deltas []HistoryValueDelta[T], key func(T) string) ([]T, error) {
	if len(deltas) == 0 {
		return state, nil
	}
	values := make(map[string]T, len(state)+len(deltas))
	for _, value := range state {
		values[key(value)] = value
	}
	for _, delta := range deltas {
		if delta.ID == "" {
			return nil, fmt.Errorf("entity delta has an empty identity")
		}
		current, exists := values[delta.ID]
		beforeFields, err := historyDeltaFields(delta.Before, delta.BeforeFields)
		if err != nil {
			return nil, err
		}
		if beforeFields == nil {
			if exists {
				return nil, fmt.Errorf("entity %s already exists", delta.ID)
			}
		} else if !exists || !historyFieldsMatch(current, beforeFields) {
			return nil, fmt.Errorf("entity %s predecessor mismatch", delta.ID)
		}
		afterFields, err := historyDeltaFields(delta.After, delta.AfterFields)
		if err != nil {
			return nil, err
		}
		if afterFields == nil {
			if !exists {
				return nil, fmt.Errorf("entity %s is missing for removal", delta.ID)
			}
			delete(values, delta.ID)
			continue
		}
		var successor T
		if exists {
			successor, err = mergeHistoryFields(current, afterFields)
		} else {
			successor, err = historyValueFromFields[T](afterFields)
		}
		if err != nil {
			return nil, err
		}
		if key(successor) != delta.ID {
			return nil, fmt.Errorf("entity %s successor identity mismatch", delta.ID)
		}
		values[delta.ID] = successor
	}
	result := make([]T, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result, nil
}

func historyDeltaFields[T any](value *T, fields map[string]any) (map[string]any, error) {
	if fields != nil {
		return fields, nil
	}
	return historyValueFields(value)
}

func historyFieldsMatch[T any](value T, fields map[string]any) bool {
	current, err := historyValueFields(&value)
	if err != nil {
		return false
	}
	for key, expected := range fields {
		actual, exists := current[key]
		if !exists && expected == nil {
			continue
		}
		if !exists || !reflect.DeepEqual(actual, expected) {
			return false
		}
	}
	return true
}

func mergeHistoryFields[T any](value T, fields map[string]any) (T, error) {
	current, err := historyValueFields(&value)
	if err != nil {
		var zero T
		return zero, err
	}
	for key, field := range fields {
		current[key] = field
	}
	return historyValueFromFields[T](current)
}

func historyValueFromFields[T any](fields map[string]any) (T, error) {
	data, err := json.Marshal(fields)
	if err != nil {
		var zero T
		return zero, err
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return value, err
	}
	return value, nil
}
