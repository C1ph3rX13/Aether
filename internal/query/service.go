package query

import (
	"github.com/C1ph3rX13/Aether/internal/database"
	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// Service provides the Project Read Model directly from canonical data.
type Service struct {
	db             *database.DB
	history        *project.HistoryQueries
	activityReader rundomain.ExecutionActivityReader
}

func NewService(db *database.DB, activityReader rundomain.ExecutionActivityReader) *Service {
	return &Service{db: db, history: project.NewHistoryQueries(), activityReader: activityReader}
}
