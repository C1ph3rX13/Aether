package api

import (
	"context"
	"net/http"

	"github.com/C1ph3rX13/Aether/internal/project"
	"github.com/gin-gonic/gin"
)

type ProjectQueries interface {
	Get(context.Context, string) (project.ProjectDetail, error)
	ListHints(context.Context, string) ([]project.Hint, error)
}

type ProjectCommands interface {
	Create(context.Context, project.CreateProjectRequest) (project.ProjectDetail, error)
	UpdateTitle(context.Context, string, project.UpdateProjectTitleRequest) (project.RenameProjectReceipt, error)
	DeleteConfirmed(context.Context, string, project.DeleteProjectRequest) (project.DeleteProjectReceipt, error)
	PauseProject(context.Context, string, project.ProjectLifecycleRequest) (project.ProjectMeta, error)
	ResumeProject(context.Context, string, project.ProjectLifecycleRequest) (project.ProjectMeta, error)
	CompleteProject(context.Context, string, project.CompleteRequest) (*project.CompleteResult, error)
	ReopenProject(context.Context, string, project.ReopenRequest) (*project.ReopenResult, error)
	RetractFact(context.Context, string, string, project.RetractFactRequest) (*project.RetractFactResult, error)
	SupersedeFact(context.Context, string, string, project.SupersedeFactRequest) (*project.SupersedeFactResult, error)
	CancelIntent(context.Context, string, string, project.CancelIntentRequest) (*project.CancelIntentResult, error)
	WithdrawHint(context.Context, string, string, project.WithdrawHintRequest) (*project.WithdrawHintResult, error)
	CreateHint(context.Context, string, project.CreateHintRequest) (*project.CreateHintResult, error)
	PreviewImpact(context.Context, string, project.ImpactRequest) (project.ImpactPreview, error)
}

// RegisterProjects exposes canonical Project/Blackboard projections and
// explicit action routes. Legacy status aliases are intentionally absent.
func RegisterProjects(
	group *gin.RouterGroup,
	projectReads ProjectReader,
	queries ProjectQueries,
	commands ProjectCommands,
	history HistoryQueries,
) {
	projects := group.Group("/projects")
	projects.GET("", listProjectCatalog(projectReads))
	projects.POST("", createProject(commands))
	projects.GET("/:id", getProject(queries))
	projects.GET("/:id/blackboard", getProject(queries))
	projects.GET("/:id/log", listProjectLog(projectReads))
	projects.GET("/:id/execution-evidence", getWorkspaceExecutionEvidence(projectReads))
	projects.GET("/:id/changes", listProjectChanges(history))
	projects.GET("/:id/history/:revision/blackboard", getProjectHistoryBlackboard(history))
	projects.POST("/:id/actions/delete", deleteProject(commands))
	projects.POST("/:id/actions/rename", renameProject(commands))
	projects.POST("/:id/actions/pause", pauseProject(commands))
	projects.POST("/:id/actions/resume", resumeProject(commands))
	projects.POST("/:id/actions/complete", completeProject(commands))
	projects.POST("/:id/actions/reopen", reopenProject(commands))
	projects.POST("/:id/actions/impact", previewProjectImpact(commands))

	facts := projects.Group("/:id/facts")
	facts.POST("/:fid/actions/retract", retractFact(commands))
	facts.POST("/:fid/actions/supersede", supersedeFact(commands))

	intents := projects.Group("/:id/intents")
	intents.POST("/:iid/actions/cancel", cancelIntent(commands))

	hints := projects.Group("/:id/hints")
	hints.GET("", listHints(queries))
	hints.POST("", createHint(commands))
	hints.POST("/:hid/actions/withdraw", withdrawHint(commands))
}

func createProject(service ProjectCommands) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request project.CreateProjectRequest
		if !bindJSON(c, &request, maximumRequestBodyBytes) {
			return
		}
		detail, err := service.Create(c.Request.Context(), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, detail)
	}
}

func getProject(service ProjectQueries) gin.HandlerFunc {
	return func(c *gin.Context) {
		detail, err := service.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, detail)
	}
}

func renameProject(service ProjectCommands) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request project.UpdateProjectTitleRequest
		if !bindProjectJSON(c, &request) {
			return
		}
		receipt, err := service.UpdateTitle(c.Request.Context(), c.Param("id"), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, receipt)
	}
}

func pauseProject(svc ProjectCommands) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request project.ProjectLifecycleRequest
		if !bindProjectJSON(c, &request) {
			return
		}
		meta, err := svc.PauseProject(c.Request.Context(), c.Param("id"), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, project.ProjectLifecycleReceipt{Project: meta, ProjectRevision: meta.Revision})
	}
}

func resumeProject(svc ProjectCommands) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request project.ProjectLifecycleRequest
		if !bindProjectJSON(c, &request) {
			return
		}
		meta, err := svc.ResumeProject(c.Request.Context(), c.Param("id"), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, project.ProjectLifecycleReceipt{Project: meta, ProjectRevision: meta.Revision})
	}
}

func deleteProject(svc ProjectCommands) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request project.DeleteProjectRequest
		if !bindProjectJSON(c, &request) {
			return
		}
		receipt, err := svc.DeleteConfirmed(c.Request.Context(), c.Param("id"), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, receipt)
	}
}

func completeProject(svc ProjectCommands) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req project.CompleteRequest
		if !bindProjectJSON(c, &req) {
			return
		}

		result, err := svc.CompleteProject(c.Request.Context(), c.Param("id"), req)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

func reopenProject(svc ProjectCommands) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req project.ReopenRequest
		if !bindProjectJSON(c, &req) {
			return
		}

		result, err := svc.ReopenProject(c.Request.Context(), c.Param("id"), req)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, project.ReopenResponse{
			Project: result.Project, ProjectRevision: result.ProjectRevision,
			Hint: result.Hint, Previous: result.PreviousCompletion,
		})
	}
}

func previewProjectImpact(service ProjectCommands) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request project.ImpactRequest
		if !bindProjectJSON(c, &request) {
			return
		}
		preview, err := service.PreviewImpact(c.Request.Context(), c.Param("id"), request)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, preview)
	}
}
