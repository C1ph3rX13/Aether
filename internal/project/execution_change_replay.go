package project

import (
	"context"
	"fmt"
	"strings"
)

type ExecutionChangeRangeQuery struct {
	ProjectID       string
	AfterRevision   int64
	ThroughRevision int64
}

type ExecutionChangeEvidence struct {
	Revision   int64
	Generation int
	Kind       ChangeKind
}

type ExecutionChangeRange struct {
	ReplayRequired bool
	Changes        []ExecutionChangeEvidence
}

// ReplayExecutionChangesBatch returns only the typed evidence needed by Run
// scheduling. The complete batch shares one global replay budget; exceeding it
// fails open without loading an unbounded history into the snapshot.
func (s *serviceCore) ReplayExecutionChangesBatch(
	ctx context.Context,
	tx DBTX,
	queries []ExecutionChangeRangeQuery,
) ([]ExecutionChangeRange, error) {
	if len(queries) > MaximumReplayChanges {
		return nil, fmt.Errorf("scheduling change range exceeds %d Projects", MaximumReplayChanges)
	}
	result := make([]ExecutionChangeRange, len(queries))
	for index := range result {
		result[index].Changes = []ExecutionChangeEvidence{}
	}
	const rangesPerQuery = 80
	total := 0
	for offset := 0; offset < len(queries); offset += rangesPerQuery {
		end := min(offset+rangesPerQuery, len(queries))
		remaining := MaximumReplayChanges - total
		branches := make([]string, 0, end-offset)
		arguments := make([]any, 0, (end-offset)*5+1)
		for index, query := range queries[offset:end] {
			branches = append(branches, `
				SELECT ? AS request_index, revision, generation, kind FROM (
					SELECT revision, generation, kind
					FROM blackboard_changes
					WHERE project_id = ? AND revision > ? AND revision <= ?
					ORDER BY revision
					LIMIT ?
				)`)
			arguments = append(arguments,
				offset+index, query.ProjectID, query.AfterRevision, query.ThroughRevision, remaining+1,
			)
		}
		arguments = append(arguments, remaining+1)
		rows, err := tx.QueryContext(ctx, `
			SELECT request_index, revision, generation, kind FROM (`+
			strings.Join(branches, " UNION ALL ")+`) LIMIT ?`, arguments...)
		if err != nil {
			return nil, fmt.Errorf("replay scheduling changes: %w", err)
		}
		for rows.Next() {
			var requestIndex int
			var change ExecutionChangeEvidence
			if err := rows.Scan(&requestIndex, &change.Revision, &change.Generation, &change.Kind); err != nil {
				rows.Close()
				return nil, err
			}
			if requestIndex < 0 || requestIndex >= len(result) {
				rows.Close()
				return nil, fmt.Errorf("scheduling change returned an unknown request index")
			}
			result[requestIndex].Changes = append(result[requestIndex].Changes, change)
			total++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if total > MaximumReplayChanges {
			for index := range result {
				result[index].ReplayRequired = queries[index].AfterRevision < queries[index].ThroughRevision
				result[index].Changes = nil
			}
			return result, nil
		}
	}
	for index, query := range queries {
		nextRevision := query.AfterRevision + 1
		for _, change := range result[index].Changes {
			if change.Revision != nextRevision {
				result[index].ReplayRequired = true
				break
			}
			nextRevision++
		}
		if nextRevision <= query.ThroughRevision {
			result[index].ReplayRequired = true
		}
		if result[index].ReplayRequired {
			result[index].Changes = nil
		}
	}
	return result, nil
}
