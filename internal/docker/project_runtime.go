package docker

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sync"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/containerd/errdefs"
	dockertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	projectReconcileMaxAttempts = 5
	projectReconcileRetryDelay  = 100 * time.Millisecond
)

const (
	projectLabelManaged    = "aether.project.managed"
	projectLabelDeployment = "aether.project.deployment_id"
	projectLabelID         = "aether.project_id"
	projectLabelRevision   = "aether.config_revision"
)

type projectContainer struct {
	mu      sync.Mutex
	id      string
	project string
	name    string
	config  ProjectConfig
}

func (r *Runtime) launchProjectExec(
	ctx context.Context,
	spec rundomain.LaunchSpec,
	registration rundomain.ExecutionHandleRegistration,
) (rundomain.Process, error) {
	if err := rundomain.ValidateLaunchSpec(spec); err != nil {
		return nil, err
	}
	containerID, err := r.ensureProjectContainer(ctx, spec, registration)
	if err != nil {
		return nil, err
	}
	inputPath := ""
	if spec.RenderedInput.Path != "" {
		inputPath = spec.RenderedInput.Path
		if err := r.writeBlackboardInput(ctx, containerID, inputPath, spec.RenderedInput.Bytes); err != nil {
			return nil, err
		}
	}
	if spec.ProtocolFile != nil {
		if err := r.writeProtocolFile(ctx, containerID, *spec.ProtocolFile); err != nil {
			return nil, err
		}
	}
	environment := spec.Environment
	command, pidFile := projectExecCommand(spec)
	limits, err := r.outputLimits(ctx, spec.ConfigRevision)
	if err != nil {
		return nil, err
	}
	exec, err := newExecProcess(r.client, containerID,
		pidFile, command, environment, limits, spec.OutputCapture)
	if err != nil {
		return nil, err
	}
	if err := exec.prepare(ctx); err != nil {
		return nil, fmt.Errorf("prepare Project Exec: %w", err)
	}
	exec.mu.Lock()
	execID := exec.execID
	exec.mu.Unlock()
	handle := rundomain.ExecutionHandle{
		RunID: spec.RunID, ProjectID: spec.ProjectID, ContainerID: containerID,
		ExecID: execID, Phase: spec.Phase, StartedAt: time.Now().UTC(),
	}
	if registration != nil {
		if err := registration.Started(ctx, handle); err != nil {
			return nil, fmt.Errorf("record Project Exec: %w", err)
		}
	}
	if err := exec.start(ctx); err != nil {
		if registration != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			markErr := registration.Stopped(cleanupCtx, handle)
			cancel()
			if markErr != nil {
				return nil, fmt.Errorf("start Project Exec: %w; mark stopped: %v", err, markErr)
			}
		}
		return nil, fmt.Errorf("start Project Exec: %w", err)
	}
	r.mu.Lock()
	if r.projectRuns[spec.RunID] == nil {
		r.projectRuns[spec.RunID] = make(map[*execProcess]struct{})
	}
	r.projectRuns[spec.RunID][exec] = struct{}{}
	r.mu.Unlock()
	process := newProjectProcess(
		r, containerID, spec.RunID, exec, spec.PhaseTimeout, spec.StopGrace, spec.Phase, registration, handle,
	)
	return process, nil
}

func projectExecCommand(spec rundomain.LaunchSpec) ([]string, string) {
	pidFile := projectExecPIDFile(spec.RunID, spec.Phase)
	// Docker Execs share the Project container, so give each phase its own
	// session/process group before handing it the worker command.
	command := []string{
		"/bin/sh", "-c",
		`umask 077
pid_file=$1
shift
exec setsid --wait /bin/sh -c 'pgid=
if [ -r "/proc/$$/stat" ]; then
IFS=" " read -r ignored1 ignored2 ignored3 ignored4 pgid ignored5 < "/proc/$$/stat" || exit 125
fi
[ -n "$pgid" ] || pgid=$$
printf "%s\n" "$pgid" > "$1" || exit 125
shift
exec "$@"' aether-exec "$pid_file" "$@"`,
		"aether-exec", pidFile,
	}
	command = append(command, spec.Command...)
	return command, pidFile
}

func projectExecPIDFile(runID rundomain.RunID, phase rundomain.ExecutionPhase) string {
	digest := sha256.Sum256([]byte(string(runID) + "\x00" + string(phase)))
	return fmt.Sprintf("/tmp/.aether-exec-%x.pid", digest[:])
}

func (r *Runtime) ensureProjectContainer(
	ctx context.Context,
	spec rundomain.LaunchSpec,
	registration rundomain.ExecutionHandleRegistration,
) (string, error) {
	r.mu.Lock()
	project := r.projects[spec.ProjectID]
	if project == nil {
		project = &projectContainer{project: spec.ProjectID, name: readableProjectName(r.options.DeploymentID, spec.ProjectID)}
		r.projects[spec.ProjectID] = project
	}
	r.mu.Unlock()
	projectConfig, err := r.projectConfig(ctx, spec.ConfigRevision)
	if err != nil {
		return "", err
	}
	project.mu.Lock()
	defer project.mu.Unlock()
	project.config = projectConfig
	if project.id != "" {
		inspection, err := r.client.ContainerInspect(ctx, project.id, client.ContainerInspectOptions{})
		if err == nil {
			if err := recordProjectContainer(ctx, spec, project.id, registration); err != nil {
				return "", err
			}
			if inspection.Container.State != nil && !inspection.Container.State.Running {
				if _, err := r.client.ContainerStart(ctx, project.id, client.ContainerStartOptions{}); err != nil {
					return "", fmt.Errorf("start Project container %s: %w", project.project, err)
				}
			}
			return project.id, nil
		}
		if !errdefs.IsNotFound(err) {
			return "", fmt.Errorf("inspect Project container %s: %w", project.project, err)
		}
		project.id = ""
	}
	inspection, err := r.client.ContainerInspect(ctx, project.name, client.ContainerInspectOptions{})
	if err == nil {
		return r.adoptProjectContainer(ctx, project, inspection, spec, registration)
	}
	if !errdefs.IsNotFound(err) {
		return "", fmt.Errorf("inspect Project container name %s: %w", project.name, err)
	}
	config := &dockertypes.Config{
		Image: project.config.Image,
		Cmd:   []string{"sleep", "infinity"},
		Labels: map[string]string{
			projectLabelManaged: "true", projectLabelDeployment: r.options.DeploymentID,
			projectLabelID: project.project, projectLabelRevision: spec.ConfigRevision,
		},
	}
	initProcess := true
	host := &dockertypes.HostConfig{
		NetworkMode: dockertypes.NetworkMode(project.config.NetworkMode),
		CapAdd:      append([]string(nil), project.config.CapAdd...),
		Init:        &initProcess,
	}
	created, err := r.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: config, HostConfig: host, Name: project.name,
	})
	if err != nil {
		if errdefs.IsConflict(err) {
			inspection, inspectErr := r.client.ContainerInspect(ctx, project.name, client.ContainerInspectOptions{})
			if inspectErr == nil {
				return r.adoptProjectContainer(ctx, project, inspection, spec, registration)
			}
			return "", fmt.Errorf("reuse conflicted Project container %s: %w", project.project, inspectErr)
		}
		return "", fmt.Errorf("create Project container %s: %w", project.project, err)
	}
	project.id = created.ID
	if recordErr := recordProjectContainer(ctx, spec, project.id, registration); recordErr != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, removeErr := r.client.ContainerRemove(cleanupCtx, created.ID, client.ContainerRemoveOptions{Force: true})
		cancel()
		project.id = ""
		if removeErr != nil && !errdefs.IsNotFound(removeErr) {
			return "", fmt.Errorf("%w; remove newly-created Project container %s: %v", recordErr, project.project, removeErr)
		}
		return "", recordErr
	}
	if _, err := r.client.ContainerStart(ctx, project.id, client.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("start Project container %s: %w", project.project, err)
	}
	return project.id, nil
}

func (r *Runtime) adoptProjectContainer(
	ctx context.Context,
	project *projectContainer,
	inspection client.ContainerInspectResult,
	spec rundomain.LaunchSpec,
	registration rundomain.ExecutionHandleRegistration,
) (string, error) {
	if !projectContainerOwnershipMatches(inspection, r.options.DeploymentID, project.project) {
		return "", fmt.Errorf("Docker container name %s is not an Aether Project container", project.name)
	}
	project.id = inspection.Container.ID
	if err := recordProjectContainer(ctx, spec, project.id, registration); err != nil {
		return "", err
	}
	if inspection.Container.State == nil || !inspection.Container.State.Running {
		if _, err := r.client.ContainerStart(ctx, project.id, client.ContainerStartOptions{}); err != nil {
			return "", fmt.Errorf("start Project container %s: %w", project.project, err)
		}
	}
	return project.id, nil
}

func projectContainerOwnershipMatches(inspection client.ContainerInspectResult, deploymentID, projectID string) bool {
	return inspection.Container.Config != nil && inspection.Container.Config.Labels[projectLabelManaged] == "true" &&
		inspection.Container.Config.Labels[projectLabelDeployment] == deploymentID &&
		inspection.Container.Config.Labels[projectLabelID] == projectID
}

func recordProjectContainer(
	ctx context.Context,
	spec rundomain.LaunchSpec,
	containerID string,
	registration rundomain.ExecutionHandleRegistration,
) error {
	if registration == nil {
		return nil
	}
	if err := registration.ContainerReady(ctx, rundomain.ProjectContainerHandle{
		RunID: spec.RunID, ProjectID: spec.ProjectID, ContainerID: containerID, RecordedAt: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("record Project container: %w", err)
	}
	return nil
}

func readableProjectName(deploymentID, projectID string) string {
	digest := sha256.Sum256([]byte(deploymentID + "\x00" + projectID))
	return formattedProjectName(projectID, digest)
}

func formattedProjectName(projectID string, digest [sha256.Size]byte) string {
	value := []byte(projectID)
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '.' || character == '-' {
			continue
		}
		value[index] = '-'
	}
	if len(value) > 32 {
		value = value[:32]
	}
	return fmt.Sprintf("aether-project-%s-%x", value, digest[:6])
}

func (r *Runtime) stopProjectRunExecs(ctx context.Context, runID rundomain.RunID) error {
	r.mu.Lock()
	execs := make([]*execProcess, 0, len(r.projectRuns[runID]))
	for exec := range r.projectRuns[runID] {
		execs = append(execs, exec)
	}
	r.mu.Unlock()
	var first error
	for _, exec := range execs {
		if err := exec.stop(ctx, r.options.DisposeGrace); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (r *Runtime) StopExecutionHandle(ctx context.Context, handle rundomain.ExecutionHandle) error {
	if handle.ContainerID == "" || handle.ExecID == "" {
		return fmt.Errorf("execution handle is incomplete")
	}
	process := &execProcess{
		client: r.client, containerID: handle.ContainerID, execID: handle.ExecID,
		pidFile: projectExecPIDFile(handle.RunID, handle.Phase),
		done:    make(chan struct{}),
	}
	return process.stop(ctx, r.options.DisposeGrace)
}

// CleanupProject disposes only runtime evidence copied into a fenced cleanup
// job and validates Aether ownership labels before removing a container.
func (r *Runtime) CleanupProject(ctx context.Context, job rundomain.ProjectCleanupJob) error {
	containers := make(map[string]struct{})
	if job.ContainerID != "" {
		containers[job.ContainerID] = struct{}{}
	}
	for _, handle := range job.Handles {
		if handle.ProjectID != job.ProjectID {
			return fmt.Errorf("cleanup handle Project identity mismatch")
		}
		if err := r.StopExecutionHandle(ctx, handle); err != nil && !errdefs.IsNotFound(err) {
			return err
		}
		containers[handle.ContainerID] = struct{}{}
	}
	for containerID := range containers {
		inspection, err := r.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
		if err != nil {
			if errdefs.IsNotFound(err) {
				continue
			}
			return err
		}
		if inspection.Container.Config == nil || inspection.Container.Config.Labels[projectLabelManaged] != "true" ||
			inspection.Container.Config.Labels[projectLabelDeployment] != r.options.DeploymentID ||
			inspection.Container.Config.Labels[projectLabelID] != job.ProjectID {
			return fmt.Errorf("container %s is not owned by cleanup Project %s", containerID, job.ProjectID)
		}
		if err := r.removeProjectContainer(ctx, containerID, job.ProjectID); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) ReconcileProjectInventory(_ context.Context, lookup rundomain.ProjectRuntimeStatusLookup) error {
	if lookup == nil {
		return fmt.Errorf("Project runtime status lookup is required")
	}
	r.reconcileMu.Lock()
	if r.reconcileClosed {
		r.reconcileMu.Unlock()
		return context.Canceled
	}
	r.reconcilePending = lookup
	if r.reconcileRunning {
		r.reconcileMu.Unlock()
		return nil
	}
	r.reconcileRunning = true
	r.workers.Add(1)
	r.reconcileMu.Unlock()
	go func() {
		defer r.workers.Done()
		r.runProjectReconciler()
	}()
	return nil
}

func (r *Runtime) runProjectReconciler() {
	failures := 0
	for {
		r.reconcileMu.Lock()
		lookup := r.reconcilePending
		r.reconcilePending = nil
		if lookup == nil {
			r.reconcileRunning = false
			r.reconcileMu.Unlock()
			return
		}
		r.reconcileMu.Unlock()
		ctx, cancel := context.WithTimeout(r.context, 30*time.Second)
		err := r.reconcileProjects(ctx, lookup)
		cancel()
		if r.context.Err() != nil {
			r.reconcileMu.Lock()
			r.reconcileRunning = false
			r.reconcileMu.Unlock()
			return
		}
		if err == nil {
			failures = 0
			continue
		}
		slog.Warn("Project container reconciliation failed", "error", err)
		r.reconcileMu.Lock()
		if r.reconcilePending == nil {
			failures++
		}
		if r.reconcilePending == nil && failures < projectReconcileMaxAttempts {
			r.reconcilePending = lookup
		} else if r.reconcilePending != nil {
			failures = 0
		}
		r.reconcileMu.Unlock()
		if failures >= projectReconcileMaxAttempts {
			slog.Error("Project container reconciliation remains unresolved",
				"attempts", failures, "error", err)
			failures = 0
			continue
		}
		timer := time.NewTimer(projectReconcileRetryDelay)
		select {
		case <-r.context.Done():
			timer.Stop()
			r.reconcileMu.Lock()
			r.reconcileRunning = false
			r.reconcileMu.Unlock()
			return
		case <-timer.C:
		}
	}
}

func (r *Runtime) reconcileProjects(ctx context.Context, lookup rundomain.ProjectRuntimeStatusLookup) error {
	containers, err := r.listProjectContainerPage(ctx, "")
	if err != nil {
		return err
	}
	for len(containers) > 0 {
		var next []dockertypes.Summary
		if len(containers) == rundomain.MaximumProjectRuntimeStatusBatch {
			cursor := containers[len(containers)-1].ID
			if cursor == "" {
				return fmt.Errorf("Project container inventory returned an empty pagination cursor")
			}
			next, err = r.listProjectContainerPage(ctx, cursor)
			if err != nil {
				return err
			}
		}
		if err := r.reconcileProjectContainerPage(ctx, containers, lookup); err != nil {
			return err
		}
		containers = next
	}
	return nil
}

func (r *Runtime) listProjectContainerPage(ctx context.Context, before string) ([]dockertypes.Summary, error) {
	filters := make(client.Filters).Add("label", projectLabelManaged+"=true", projectLabelDeployment+"="+r.options.DeploymentID)
	if before != "" {
		filters.Add("before", before)
	}
	result, err := r.client.ContainerList(ctx, client.ContainerListOptions{
		All: true, Limit: rundomain.MaximumProjectRuntimeStatusBatch, Filters: filters,
	})
	if err != nil {
		return nil, fmt.Errorf("list Project containers: %w", err)
	}
	containers := result.Items
	if len(containers) > rundomain.MaximumProjectRuntimeStatusBatch {
		return nil, fmt.Errorf("Project container inventory exceeded its bounded page size")
	}
	return containers, nil
}

func (r *Runtime) reconcileProjectContainerPage(
	ctx context.Context,
	containers []dockertypes.Summary,
	lookup rundomain.ProjectRuntimeStatusLookup,
) error {
	projectIDs := make([]string, 0, len(containers))
	seenProjects := make(map[string]struct{}, len(containers))
	for _, summary := range containers {
		projectID := summary.Labels[projectLabelID]
		if projectID == "" {
			continue
		}
		if _, seen := seenProjects[projectID]; !seen {
			seenProjects[projectID] = struct{}{}
			projectIDs = append(projectIDs, projectID)
		}
	}
	statuses := make(map[string]rundomain.ProjectRuntimeStatus, len(projectIDs))
	if len(projectIDs) > 0 {
		batch, err := lookup(ctx, projectIDs)
		if err != nil {
			return fmt.Errorf("query Project runtime status batch: %w", err)
		}
		if err := rundomain.ValidateProjectRuntimeStatusResults(projectIDs, batch); err != nil {
			return err
		}
		for _, result := range batch {
			statuses[result.ProjectID] = result.Status
		}
	}
	for _, summary := range containers {
		projectID := summary.Labels[projectLabelID]
		if projectID == "" {
			continue
		}
		r.mu.Lock()
		project := r.projects[projectID]
		if project == nil {
			project = &projectContainer{project: projectID, name: readableProjectName(r.options.DeploymentID, projectID), config: r.options.Project}
			r.projects[projectID] = project
		}
		r.mu.Unlock()
		project.mu.Lock()
		project.id = summary.ID
		projectConfig := project.config
		project.mu.Unlock()
		if revision := summary.Labels[projectLabelRevision]; revision != "" && r.options.ProjectConfigForRevision != nil {
			loaded, err := r.projectConfig(ctx, revision)
			if err != nil {
				return err
			}
			projectConfig = loaded
			project.mu.Lock()
			project.config = loaded
			project.mu.Unlock()
		}
		status, exists := statuses[projectID]
		switch {
		case !exists || status == rundomain.ProjectRuntimeMissing:
			if err := r.removeProjectContainer(ctx, summary.ID, projectID); err != nil {
				return err
			}
		case status == rundomain.ProjectRuntimePaused:
			if err := r.stopProjectContainer(ctx, summary.ID, projectID); err != nil {
				return err
			}
		case status == rundomain.ProjectRuntimeCompleted:
			if projectConfig.CompletedAction == "remove" {
				if err := r.removeProjectContainer(ctx, summary.ID, projectID); err != nil {
					return err
				}
			} else if err := r.stopProjectContainer(ctx, summary.ID, projectID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runtime) stopProjectContainer(ctx context.Context, id, projectID string) error {
	timeout := int(r.options.DisposeGrace / time.Second)
	if timeout <= 0 {
		timeout = 1
	}
	if _, err := r.client.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &timeout}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("stop Project container %s: %w", projectID, err)
	}
	return nil
}

func (r *Runtime) removeProjectContainer(ctx context.Context, id, projectID string) error {
	if _, err := r.client.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("remove Project container %s: %w", projectID, err)
	}
	r.mu.Lock()
	delete(r.projects, projectID)
	r.mu.Unlock()
	return nil
}

func (r *Runtime) forgetProjectExec(runID rundomain.RunID, exec *execProcess) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if runs := r.projectRuns[runID]; runs != nil {
		delete(runs, exec)
		if len(runs) == 0 {
			delete(r.projectRuns, runID)
		}
	}
}
