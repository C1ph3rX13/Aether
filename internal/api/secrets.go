package api

import (
	"context"
	"net/http"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/gin-gonic/gin"
)

type RunSecretResolver interface {
	ResolveRunSecrets(context.Context, rundomain.RunSecretRequest) (*rundomain.ResolvedSecrets, error)
}

// RegisterRunSecrets exposes only the server-authorized, Run-scoped
// Secret operation. The returned values are transient execution material and
// are never written to the Run ledger or an operational projection.
func RegisterRunSecrets(group *gin.RouterGroup, service RunSecretResolver) {
	group.POST("/runs/:run_id/actions/resolve-secrets", func(c *gin.Context) {
		var request rundomain.RunSecretRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		if string(request.RunID) != c.Param("run_id") {
			writePathIdentityError(c, "Run")
			return
		}
		secrets, err := service.ResolveRunSecrets(c.Request.Context(), request)
		if err != nil {
			writeError(c, err)
			return
		}
		if secrets == nil {
			writeError(c, &rundomain.DomainError{Code: rundomain.ErrorInternal, Detail: "Secret Store returned no result"})
			return
		}
		environment := secrets.Environment()
		secrets.Close()
		c.JSON(http.StatusOK, gin.H{"environment": environment})
	})
}

type RevisionSecretResolver interface {
	ResolveRevisionSecrets(context.Context, string, string, rundomain.DispatcherLease) (*rundomain.ResolvedSecrets, error)
}

type revisionSecretRequest struct {
	InstanceID   string                 `json:"instance_id"`
	FencingToken rundomain.FencingToken `json:"fencing_token"`
}

// RegisterRevisionSecrets supports bounded Provider readiness checks.
// A live Dispatcher Lease is required before a Revision Secret can be read.
func RegisterRevisionSecrets(group *gin.RouterGroup, service RevisionSecretResolver) {
	group.POST("/configuration/revisions/:revision/workers/:worker/actions/resolve-secrets", func(c *gin.Context) {
		var request revisionSecretRequest
		if !bindJSON(c, &request, runControlSmallBodyBytes) {
			return
		}
		secrets, err := service.ResolveRevisionSecrets(c.Request.Context(), c.Param("revision"), c.Param("worker"), rundomain.DispatcherLease{
			InstanceID: request.InstanceID, FencingToken: request.FencingToken,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		if secrets == nil {
			writeError(c, &rundomain.DomainError{Code: rundomain.ErrorInternal, Detail: "Secret Store returned no result"})
			return
		}
		environment := secrets.Environment()
		secrets.Close()
		c.JSON(http.StatusOK, gin.H{"environment": environment})
	})
}
