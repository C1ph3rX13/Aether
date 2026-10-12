package query

import (
	"github.com/C1ph3rX13/Aether/internal/database"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func newQueryService(db *database.DB) *Service {
	return NewService(db, rundomain.NewService(db, nil, nil, rundomain.ServiceOptions{}))
}
