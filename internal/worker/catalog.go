package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type CatalogSource struct {
	ImageReference string
	PromptGroup    string
	Workers        []CatalogWorker
	Prompts        map[rundomain.TaskType]PromptSet
}

type CatalogWorker struct {
	Name             string
	Adapter          string
	Model            string
	TaskTypes        []rundomain.TaskType
	MaxRunning       int
	Priority         int
	Environment      map[string]string
	Capabilities     []string
	SecretReferences []rundomain.SecretReference
}

type PromptSet struct {
	Primary          string
	Fallback         string
	RecoveryPrimary  string
	RecoveryConclude string
	Finalization     string
}

type Catalog struct {
	revision string
	source   CatalogSource
}

func NewCatalog(source CatalogSource) (*Catalog, error) {
	normalized, err := normalizeCatalog(source)
	if err != nil {
		return nil, err
	}
	identities, err := catalogSubmissionIdentities(normalized, rundomain.TaskReason, rundomain.TaskExplore, rundomain.TaskBootstrap)
	if err != nil {
		return nil, err
	}
	content, err := json.Marshal(struct {
		SubmissionIdentities map[string]submissionIdentity `json:"submission_identities"`
		Source               CatalogSource                 `json:"source"`
	}{
		SubmissionIdentities: identities,
		Source:               normalized,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal Worker Catalog: %w", err)
	}
	digest := sha256.Sum256(content)
	return &Catalog{
		revision: "sha256:" + hex.EncodeToString(digest[:]), source: normalized,
	}, nil
}

func NewCatalogForRevision(revision string, source CatalogSource) (*Catalog, error) {
	if strings.TrimSpace(revision) == "" || len(revision) > 256 {
		return nil, fmt.Errorf("Worker Catalog Config Revision is invalid")
	}
	catalog, err := NewCatalog(source)
	if err != nil {
		return nil, err
	}
	catalog.revision = revision
	return catalog, nil
}

func (catalog *Catalog) Revision() string {
	if catalog == nil {
		return ""
	}
	return catalog.revision
}

func (catalog *Catalog) ImageReference() string {
	if catalog == nil {
		return ""
	}
	return catalog.source.ImageReference
}

func (catalog *Catalog) Worker(name string) (CatalogWorker, bool) {
	if catalog == nil {
		return CatalogWorker{}, false
	}
	for _, worker := range catalog.source.Workers {
		if worker.Name == name {
			return cloneCatalogWorker(worker), true
		}
	}
	return CatalogWorker{}, false
}

func (catalog *Catalog) Workers() []CatalogWorker {
	if catalog == nil {
		return nil
	}
	workers := make([]CatalogWorker, len(catalog.source.Workers))
	for index, worker := range catalog.source.Workers {
		workers[index] = cloneCatalogWorker(worker)
	}
	return workers
}

func (catalog *Catalog) Adapters() []string {
	seen := make(map[string]struct{})
	for _, worker := range catalog.Workers() {
		seen[worker.Adapter] = struct{}{}
	}
	adapters := make([]string, 0, len(seen))
	for adapter := range seen {
		adapters = append(adapters, adapter)
	}
	sort.Strings(adapters)
	return adapters
}

func (catalog *Catalog) WorkerSnapshot(name string) (rundomain.WorkerSnapshot, error) {
	worker, ok := catalog.Worker(name)
	if !ok {
		return rundomain.WorkerSnapshot{}, fmt.Errorf("Worker %q is unavailable", name)
	}
	return rundomain.WorkerSnapshot{
		Name: worker.Name, Adapter: worker.Adapter, Model: worker.Model,
		Capabilities: worker.Capabilities, SecretReferences: worker.SecretReferences,
	}, nil
}

func (catalog *Catalog) WorkerSnapshotForTask(name string, task rundomain.TaskType) (rundomain.WorkerSnapshot, error) {
	worker, ok := catalog.Worker(name)
	if !ok {
		return rundomain.WorkerSnapshot{}, fmt.Errorf("Worker %q is unavailable", name)
	}
	if !slices.Contains(worker.TaskTypes, task) {
		return rundomain.WorkerSnapshot{}, fmt.Errorf("Worker %q does not support task %s", name, task)
	}
	return rundomain.WorkerSnapshot{
		Name: worker.Name, Adapter: worker.Adapter, Model: worker.Model,
		Capabilities: worker.Capabilities, SecretReferences: worker.SecretReferences,
	}, nil
}

func (catalog *Catalog) AgentAdapter(name string) (AgentAdapter, error) {
	worker, ok := catalog.Worker(name)
	if !ok {
		return nil, fmt.Errorf("Worker %q is unavailable", name)
	}
	return NewAgentAdapter(WorkerConfig{
		Name: worker.Name, Type: WorkerType(worker.Adapter), Env: cloneEnvironment(worker.Environment),
	})
}

func (catalog *Catalog) Prompt(task rundomain.TaskType) (PromptSet, bool) {
	if catalog == nil {
		return PromptSet{}, false
	}
	prompt, ok := catalog.source.Prompts[task]
	return prompt, ok
}

func (catalog *Catalog) PromptTemplateHash(task rundomain.TaskType) (string, error) {
	prompt, ok := catalog.Prompt(task)
	if !ok {
		return "", fmt.Errorf("prompt for %s is unavailable", task)
	}
	identities, err := catalogSubmissionIdentities(catalog.source, task)
	if err != nil {
		return "", err
	}
	material := struct {
		Schema               string                        `json:"schema"`
		Task                 string                        `json:"task"`
		PromptGroup          string                        `json:"prompt_group"`
		SubmissionIdentities map[string]submissionIdentity `json:"submission_identities"`
		Prompts              PromptSet                     `json:"prompts"`
	}{
		Schema:               "aether.effective-prompt-set/v1",
		Task:                 string(task),
		PromptGroup:          catalog.source.PromptGroup,
		SubmissionIdentities: identities,
		Prompts:              prompt,
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		return "", fmt.Errorf("marshal prompt identity for %s: %w", task, err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

type CatalogPublisher struct{ current atomic.Pointer[Catalog] }

func (publisher *CatalogPublisher) Publish(source CatalogSource) (*Catalog, error) {
	catalog, err := NewCatalog(source)
	if err != nil {
		return nil, err
	}
	publisher.current.Store(catalog)
	return catalog, nil
}

func (publisher *CatalogPublisher) Reload(load func() (CatalogSource, error)) (*Catalog, error) {
	if load == nil {
		return nil, fmt.Errorf("Catalog loader is required")
	}
	source, err := load()
	if err != nil {
		return nil, err
	}
	return publisher.Publish(source)
}

func (publisher *CatalogPublisher) Current() *Catalog { return publisher.current.Load() }

func catalogSubmissionIdentities(source CatalogSource, tasks ...rundomain.TaskType) (map[string]submissionIdentity, error) {
	identities := make(map[string]submissionIdentity)
	for _, worker := range source.Workers {
		if _, exists := identities[worker.Adapter]; exists {
			continue
		}
		definition, err := LookupAdapterDefinition(worker.Adapter)
		if err != nil {
			return nil, err
		}
		identities[worker.Adapter] = definition.submission.submissionIdentity(tasks...)
	}
	return identities, nil
}

func normalizeCatalog(source CatalogSource) (CatalogSource, error) {
	source.ImageReference = strings.TrimSpace(source.ImageReference)
	if source.ImageReference == "" || len(source.ImageReference) > 512 {
		return CatalogSource{}, fmt.Errorf("Agent Runtime Image reference is required")
	}
	if len(source.Workers) > 256 {
		return CatalogSource{}, fmt.Errorf("Worker Catalog cannot exceed 256 workers")
	}
	source.Workers = append([]CatalogWorker(nil), source.Workers...)
	seenNames := make(map[string]struct{}, len(source.Workers))
	for index := range source.Workers {
		worker, err := normalizeCatalogWorker(source.Workers[index])
		if err != nil {
			return CatalogSource{}, err
		}
		if _, duplicate := seenNames[worker.Name]; duplicate {
			return CatalogSource{}, fmt.Errorf("Worker names must be unique")
		}
		seenNames[worker.Name] = struct{}{}
		source.Workers[index] = worker
	}
	sort.Slice(source.Workers, func(i, j int) bool { return source.Workers[i].Name < source.Workers[j].Name })

	prompts := make(map[rundomain.TaskType]PromptSet, len(source.Prompts))
	for _, task := range []rundomain.TaskType{rundomain.TaskReason, rundomain.TaskExplore, rundomain.TaskBootstrap} {
		prompt, ok := source.Prompts[task]
		if !ok || strings.TrimSpace(prompt.Primary) == "" || len(prompt.Primary) > maxPromptTemplateBytes {
			return CatalogSource{}, fmt.Errorf("primary prompt for %s is required", task)
		}
		if (task == rundomain.TaskBootstrap || task == rundomain.TaskExplore) &&
			(strings.TrimSpace(prompt.Fallback) == "" || len(prompt.Fallback) > maxPromptTemplateBytes) {
			return CatalogSource{}, fmt.Errorf("%s fallback prompt is required", task)
		}
		if task == rundomain.TaskReason && prompt.Fallback != "" {
			return CatalogSource{}, fmt.Errorf("Reason cannot define a fallback prompt")
		}
		if strings.TrimSpace(prompt.Finalization) == "" || len(prompt.Finalization) > maxPromptTemplateBytes {
			return CatalogSource{}, fmt.Errorf("Finalization prompt for %s is required", task)
		}
		prompts[task] = prompt
	}
	source.Prompts = prompts
	return source, nil
}

func normalizeCatalogWorker(worker CatalogWorker) (CatalogWorker, error) {
	worker.Name = strings.TrimSpace(worker.Name)
	worker.Adapter = strings.TrimSpace(worker.Adapter)
	worker.Model = strings.TrimSpace(worker.Model)
	if worker.Name == "" || len(worker.Name) > 256 || worker.Adapter == "" || len(worker.Adapter) > 128 ||
		len(worker.Model) > 256 || worker.MaxRunning <= 0 || worker.Priority < 0 || len(worker.TaskTypes) == 0 {
		return CatalogWorker{}, fmt.Errorf("Worker %q has invalid identity or scheduling limits", worker.Name)
	}
	definition, err := LookupAdapterDefinition(worker.Adapter)
	if err != nil {
		return CatalogWorker{}, fmt.Errorf("Worker %q uses unsupported Adapter %q", worker.Name, worker.Adapter)
	}
	worker.TaskTypes = append([]rundomain.TaskType(nil), worker.TaskTypes...)
	sort.Slice(worker.TaskTypes, func(i, j int) bool { return worker.TaskTypes[i] < worker.TaskTypes[j] })
	for index, task := range worker.TaskTypes {
		if task != rundomain.TaskReason && task != rundomain.TaskExplore && task != rundomain.TaskBootstrap ||
			index > 0 && worker.TaskTypes[index-1] == task {
			return CatalogWorker{}, fmt.Errorf("Worker %q task capabilities are invalid", worker.Name)
		}
	}
	worker.Environment = cloneEnvironment(worker.Environment)
	for key, value := range worker.Environment {
		if key == "" || len(key) > 128 || strings.ContainsAny(key, "=\x00") || strings.IndexByte(value, 0) >= 0 || value == "" {
			return CatalogWorker{}, fmt.Errorf("Worker %q environment is invalid", worker.Name)
		}
		if reservedEnvironmentVariables[key] {
			return CatalogWorker{}, fmt.Errorf("Worker %q environment variable %s is managed by Aether", worker.Name, key)
		}
		if definition.isSecretEnvironmentVariable(key) {
			return CatalogWorker{}, fmt.Errorf("Worker %q environment variable %s contains a Secret value", worker.Name, key)
		}
	}
	if modelVariable := definition.ModelEnvironmentVariable(); modelVariable != "" {
		if worker.Model == "" {
			return CatalogWorker{}, fmt.Errorf("Worker %q model is required for Adapter %s", worker.Name, worker.Adapter)
		}
		if configured := strings.TrimSpace(worker.Environment[modelVariable]); configured != "" && configured != worker.Model {
			return CatalogWorker{}, fmt.Errorf("Worker %q model does not match %s", worker.Name, modelVariable)
		}
		worker.Environment[modelVariable] = worker.Model
	}
	if err := definition.validateConfiguredEnvironment(worker.Name, worker.Environment); err != nil {
		return CatalogWorker{}, err
	}
	worker.Capabilities = append([]string(nil), worker.Capabilities...)
	sort.Strings(worker.Capabilities)
	for index, capability := range worker.Capabilities {
		if strings.TrimSpace(capability) == "" || len(capability) > 128 ||
			index > 0 && worker.Capabilities[index-1] == capability {
			return CatalogWorker{}, fmt.Errorf("Worker %q capabilities are invalid", worker.Name)
		}
		if capability == rundomain.ProcessRecoveryCapability && !definition.driver.hasRecovery() {
			return CatalogWorker{}, fmt.Errorf("Worker %q advertises process recovery but Adapter %s does not support it", worker.Name, worker.Adapter)
		}
	}
	worker.SecretReferences = append([]rundomain.SecretReference(nil), worker.SecretReferences...)
	sort.Slice(worker.SecretReferences, func(i, j int) bool {
		return worker.SecretReferences[i].EnvironmentVariable < worker.SecretReferences[j].EnvironmentVariable
	})
	seenSecrets := make(map[string]struct{}, len(worker.SecretReferences))
	for _, reference := range worker.SecretReferences {
		if reference.EnvironmentVariable == "" || strings.TrimSpace(reference.EnvironmentVariable) != reference.EnvironmentVariable ||
			strings.ContainsAny(reference.EnvironmentVariable, "=\x00") || reference.Provider == "" ||
			strings.TrimSpace(reference.Provider) != reference.Provider || strings.ContainsRune(reference.Provider, '\x00') ||
			reference.Key == "" || strings.TrimSpace(reference.Key) != reference.Key || strings.ContainsRune(reference.Key, '\x00') {
			return CatalogWorker{}, fmt.Errorf("Worker %q Secret Reference is invalid", worker.Name)
		}
		if reservedEnvironmentVariables[reference.EnvironmentVariable] ||
			definition.isValidatedEnvironmentVariable(reference.EnvironmentVariable) &&
				!definition.isSecretEnvironmentVariable(reference.EnvironmentVariable) {
			return CatalogWorker{}, fmt.Errorf("Worker %q Secret Reference targets a controlled environment variable %s", worker.Name, reference.EnvironmentVariable)
		}
		if _, duplicate := seenSecrets[reference.EnvironmentVariable]; duplicate {
			return CatalogWorker{}, fmt.Errorf("Worker %q Secret References are duplicated", worker.Name)
		}
		seenSecrets[reference.EnvironmentVariable] = struct{}{}
	}
	for _, required := range definition.requiredSecrets {
		if _, ok := seenSecrets[required]; !ok {
			return CatalogWorker{}, fmt.Errorf("Worker %q is missing Secret Reference %s", worker.Name, required)
		}
	}
	return worker, nil
}

func cloneCatalogWorker(worker CatalogWorker) CatalogWorker {
	worker.TaskTypes = append([]rundomain.TaskType(nil), worker.TaskTypes...)
	worker.Environment = cloneEnvironment(worker.Environment)
	worker.Capabilities = append([]string(nil), worker.Capabilities...)
	worker.SecretReferences = append([]rundomain.SecretReference(nil), worker.SecretReferences...)
	return worker
}

func cloneEnvironment(environment map[string]string) map[string]string {
	result := make(map[string]string, len(environment))
	for key, value := range environment {
		result[key] = value
	}
	return result
}

var reservedEnvironmentVariables = map[string]bool{
	"AETHER_RESULT_PROTOCOL": true, "HOME": true, "LD_LIBRARY_PATH": true, "LD_PRELOAD": true,
	"MOCK_CONTEXT": true, "MOCK_PHASE": true, "PATH": true, "PWD": true, "SHELL": true, "TMPDIR": true,
}
