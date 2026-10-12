package run

import (
	"context"
	"time"

	"github.com/C1ph3rX13/Aether/internal/database"
)

// AdmissionAuthorization is the authoritative execution material selected by
// the bound Config Revision. The caller may choose only the Worker identity;
// all other values are rebuilt by the Control Plane.
type AdmissionAuthorization struct {
	Spec        AdmissionSpecSnapshot
	RetryPolicy RetryPolicy
}

// AdmissionAuthority is called while Run Admission owns the SQLite write
// transaction. Implementations must perform bounded local reads only. Provider,
// Docker, Prompt rendering, Secret resolution, and network I/O belong later.
type AdmissionAuthority interface {
	AuthorizeRunAdmission(
		context.Context,
		database.DBTX,
		StartRequest,
		ProjectAdmission,
		time.Time,
	) (AdmissionAuthorization, error)
}
