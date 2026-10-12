package project

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const (
	MaximumSchedulingReadProjects = 1024
	MaximumSchedulingReadIntents  = MaximumSchedulingReadProjects * 3
	maximumSchedulingCursorBytes  = 256
)

// SchedulingReadRequest bounds the Project-owned part of one Scheduling
// Snapshot. The caller supplies the transaction so Project and Run evidence
// can share one SQLite observation.
type SchedulingReadRequest struct {
	ProjectCursor      string
	ProjectLimit       int
	BootstrapSupported bool
	ReasonSupported    bool
}

func (request SchedulingReadRequest) validate() error {
	if len(request.ProjectCursor) > maximumSchedulingCursorBytes ||
		request.ProjectLimit <= 0 || request.ProjectLimit > MaximumSchedulingReadProjects {
		return fmt.Errorf("Project scheduling read request is invalid")
	}
	return nil
}

// SchedulingProjectReadModel contains only Project-owned scheduling evidence.
// Run claims, retry state, cooldowns, and Work Keys are deliberately absent.
type SchedulingProjectReadModel struct {
	ID                    string
	ConfigRevision        string
	Generation            int
	Revision              int64
	BootstrapEnabled      bool
	HasOpenIntents        bool
	Initial               bool
	HasBootstrapIntent    bool
	OpenBootstrapIntentID string
	Decision              SchedulingDecision
}

type SchedulingReadModel struct {
	Projects          []SchedulingProjectReadModel
	NextProjectCursor string
}

type SchedulingDecisionKind string

const (
	SchedulingDecisionNone             SchedulingDecisionKind = ""
	SchedulingDecisionBootstrap        SchedulingDecisionKind = "bootstrap"
	SchedulingDecisionReason           SchedulingDecisionKind = "reason"
	SchedulingDecisionMissingBootstrap SchedulingDecisionKind = "missing_bootstrap"
)

type SchedulingDecision struct {
	Kind           SchedulingDecisionKind
	TargetIntentID string
}

func (project SchedulingProjectReadModel) schedulingDecision(
	bootstrapSupported,
	reasonSupported bool,
) SchedulingDecision {
	if project.BootstrapEnabled && project.Initial && bootstrapSupported {
		if project.OpenBootstrapIntentID != "" {
			return SchedulingDecision{Kind: SchedulingDecisionBootstrap, TargetIntentID: project.OpenBootstrapIntentID}
		}
		if !project.HasBootstrapIntent {
			return SchedulingDecision{Kind: SchedulingDecisionMissingBootstrap}
		}
	}
	if reasonSupported {
		return SchedulingDecision{Kind: SchedulingDecisionReason}
	}
	return SchedulingDecision{Kind: SchedulingDecisionNone}
}

// SchedulingIntentReadModel carries only the stable identity and business
// ordering needed to derive an Explore candidate.
type SchedulingIntentReadModel struct {
	ProjectID string
	ID        string
	CreatedAt string
}

type schedulingIntentWindow struct {
	ProjectID      string
	AfterCreatedAt string
	AfterID        string
}

type SchedulingIntentReadRequest struct {
	ProjectIDs []string
	Limit      int
}

// ReadSchedulingModel returns a bounded Project-native scheduling window in a
// caller-owned transaction. It does not inspect or reconstruct Run state.
func (s *Service) ReadSchedulingModel(
	ctx context.Context,
	tx DBTX,
	request SchedulingReadRequest,
) (SchedulingReadModel, error) {
	if err := request.validate(); err != nil {
		return SchedulingReadModel{}, err
	}
	projects, err := listSchedulingProjects(ctx, tx, request.ProjectCursor, request.ProjectLimit)
	if err != nil {
		return SchedulingReadModel{}, fmt.Errorf("list bounded scheduling Projects: %w", err)
	}
	result := SchedulingReadModel{Projects: make([]SchedulingProjectReadModel, len(projects))}
	for index, item := range projects {
		item.Decision = item.schedulingDecision(request.BootstrapSupported, request.ReasonSupported)
		result.Projects[index] = item
		result.NextProjectCursor = item.ID
	}
	return result, nil
}

// ReadSchedulingIntents returns the bounded, fair Open Explore window for the
// Project subset whose Decision Work does not currently block exploration.
// The SQL eligibility filter preserves the established pre-limit semantics;
// Run remains authoritative and projects the returned candidates again.
func (s *Service) ReadSchedulingIntents(
	ctx context.Context,
	conn DBTX,
	request SchedulingIntentReadRequest,
) ([]SchedulingIntentReadModel, error) {
	if len(request.ProjectIDs) == 0 {
		return []SchedulingIntentReadModel{}, nil
	}
	if len(request.ProjectIDs) > MaximumSchedulingReadProjects ||
		request.Limit <= 0 || request.Limit > MaximumSchedulingReadIntents {
		return nil, fmt.Errorf("Project scheduling Intent request is invalid")
	}
	seen := make(map[string]bool, len(request.ProjectIDs))
	for _, projectID := range request.ProjectIDs {
		if projectID == "" || len(projectID) > maximumSchedulingCursorBytes || seen[projectID] {
			return nil, fmt.Errorf("Project scheduling Intent identities are invalid")
		}
		seen[projectID] = true
	}
	windows := make([]schedulingIntentWindow, len(request.ProjectIDs))
	for index, projectID := range request.ProjectIDs {
		windows[index] = schedulingIntentWindow{ProjectID: projectID}
	}
	result := make([]SchedulingIntentReadModel, 0, request.Limit)
	for len(result) < request.Limit && len(windows) > 0 {
		remaining := request.Limit - len(result)
		perProjectLimit := max(1, remaining/len(windows))
		batch, err := querySchedulingIntentWindows(ctx, conn, windows, perProjectLimit)
		if err != nil {
			return nil, err
		}
		byProject := make(map[string][]SchedulingIntentReadModel, len(windows))
		for _, intent := range batch {
			byProject[intent.ProjectID] = append(byProject[intent.ProjectID], intent)
		}
		nextWindows := make([]schedulingIntentWindow, 0, len(windows))
		for round := 0; len(result) < request.Limit; round++ {
			added := false
			for _, window := range windows {
				items := byProject[window.ProjectID]
				if round >= len(items) {
					continue
				}
				result = append(result, items[round])
				added = true
				if len(result) == request.Limit {
					break
				}
			}
			if !added {
				break
			}
		}
		for _, window := range windows {
			items := byProject[window.ProjectID]
			if len(items) != perProjectLimit {
				continue
			}
			last := items[len(items)-1]
			nextWindows = append(nextWindows, schedulingIntentWindow{
				ProjectID: window.ProjectID, AfterCreatedAt: last.CreatedAt, AfterID: last.ID,
			})
		}
		windows = nextWindows
	}
	return result, nil
}

// listSchedulingProjects traverses active Projects after cursor and wraps once.
// At most limit Projects are read, even when the cursor names a deleted Project.
func listSchedulingProjects(
	ctx context.Context,
	conn DBTX,
	cursor string,
	limit int,
) ([]SchedulingProjectReadModel, error) {
	if limit <= 0 {
		return []SchedulingProjectReadModel{}, nil
	}
	projects, err := querySchedulingProjects(ctx, conn, "p.id > ?", cursor, limit)
	if err != nil {
		return nil, err
	}
	if len(projects) == limit {
		return projects, nil
	}
	wrapped, err := querySchedulingProjects(ctx, conn, "p.id <= ?", cursor, limit-len(projects))
	if err != nil {
		return nil, err
	}
	return append(projects, wrapped...), nil
}

func querySchedulingProjects(
	ctx context.Context,
	conn DBTX,
	cursorPredicate string,
	cursor string,
	limit int,
) ([]SchedulingProjectReadModel, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT p.id, COALESCE(p.config_revision, ''), p.generation, p.blackboard_revision, p.bootstrap_enabled,
			EXISTS(SELECT 1 FROM intents open_intent
			 WHERE open_intent.project_id = p.id AND open_intent.kind = 'explore'
			   AND open_intent.to_fact_id IS NULL AND open_intent.cancelled_at IS NULL),
			EXISTS(SELECT 1 FROM facts origin_fact
			 WHERE origin_fact.project_id = p.id AND origin_fact.id = 'origin'
			   AND origin_fact.retracted_at IS NULL),
			EXISTS(SELECT 1 FROM facts goal_fact
			 WHERE goal_fact.project_id = p.id AND goal_fact.id = 'goal'
			   AND goal_fact.retracted_at IS NULL),
			EXISTS(SELECT 1 FROM facts ordinary_fact
			 WHERE ordinary_fact.project_id = p.id AND ordinary_fact.retracted_at IS NULL
			   AND ordinary_fact.id NOT IN ('origin', 'goal')),
			EXISTS(SELECT 1 FROM intents ordinary_intent
			 WHERE ordinary_intent.project_id = p.id AND ordinary_intent.kind <> 'bootstrap'),
			COALESCE((SELECT bootstrap_intent.id FROM intents bootstrap_intent
			 WHERE bootstrap_intent.project_id = p.id AND bootstrap_intent.kind = 'bootstrap'
			 ORDER BY bootstrap_intent.created_at, bootstrap_intent.id LIMIT 1), ''),
			COALESCE((SELECT open_bootstrap.id FROM intents open_bootstrap
			 WHERE open_bootstrap.project_id = p.id AND open_bootstrap.kind = 'bootstrap'
			   AND open_bootstrap.to_fact_id IS NULL AND open_bootstrap.cancelled_at IS NULL
			 ORDER BY open_bootstrap.created_at, open_bootstrap.id LIMIT 1), '')
		FROM projects p
		WHERE p.status = 'active' AND `+cursorPredicate+`
		ORDER BY p.id
		LIMIT ?`, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := make([]SchedulingProjectReadModel, 0, limit)
	for rows.Next() {
		var project SchedulingProjectReadModel
		var bootstrapEnabled int
		var hasOpenIntents, hasOrigin, hasGoal, hasOrdinaryFacts, hasOrdinaryIntents int
		var bootstrapIntentID string
		if err := rows.Scan(
			&project.ID, &project.ConfigRevision, &project.Generation, &project.Revision, &bootstrapEnabled,
			&hasOpenIntents, &hasOrigin, &hasGoal, &hasOrdinaryFacts, &hasOrdinaryIntents,
			&bootstrapIntentID, &project.OpenBootstrapIntentID,
		); err != nil {
			return nil, err
		}
		project.BootstrapEnabled = bootstrapEnabled == 1
		project.HasOpenIntents = hasOpenIntents == 1
		project.Initial = hasOrigin == 1 && hasGoal == 1 && hasOrdinaryFacts == 0 && hasOrdinaryIntents == 0
		project.HasBootstrapIntent = bootstrapIntentID != ""
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

func querySchedulingIntentWindows(
	ctx context.Context,
	conn DBTX,
	windows []schedulingIntentWindow,
	perProjectLimit int,
) ([]SchedulingIntentReadModel, error) {
	const projectsPerQuery = 80
	result := make([]SchedulingIntentReadModel, 0, len(windows)*perProjectLimit)
	for offset := 0; offset < len(windows); offset += projectsPerQuery {
		end := min(offset+projectsPerQuery, len(windows))
		branches := make([]string, 0, end-offset)
		arguments := make([]any, 0, (end-offset)*6)
		for _, window := range windows[offset:end] {
			branches = append(branches, `
				SELECT project_id, id, created_at FROM (
					SELECT intent.project_id, intent.id, intent.created_at
					FROM intents intent
					WHERE intent.project_id = ? AND intent.kind = 'explore'
					  AND intent.to_fact_id IS NULL AND intent.cancelled_at IS NULL
					  AND (? = '' OR intent.created_at > ? OR (intent.created_at = ? AND intent.id > ?))
					ORDER BY intent.created_at, intent.id
					LIMIT ?
				)`)
			arguments = append(arguments,
				window.ProjectID,
				window.AfterCreatedAt, window.AfterCreatedAt, window.AfterCreatedAt, window.AfterID,
				perProjectLimit,
			)
		}
		rows, err := conn.QueryContext(ctx, strings.Join(branches, " UNION ALL "), arguments...)
		if err != nil {
			return nil, fmt.Errorf("list scheduling Intents: %w", err)
		}
		for rows.Next() {
			var intent SchedulingIntentReadModel
			if err := rows.Scan(&intent.ProjectID, &intent.ID, &intent.CreatedAt); err != nil {
				rows.Close()
				return nil, err
			}
			result = append(result, intent)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].ProjectID != result[right].ProjectID {
			return result[left].ProjectID < result[right].ProjectID
		}
		if result[left].CreatedAt != result[right].CreatedAt {
			return result[left].CreatedAt < result[right].CreatedAt
		}
		return result[left].ID < result[right].ID
	})
	return result, nil
}
