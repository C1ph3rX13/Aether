package worker

import (
	"fmt"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type CatalogConfiguration struct {
	Revision       string
	ImageReference string
	PromptGroup    string
	Workers        []WorkerConfig
}

// CatalogSourceFromConfig builds the immutable Worker Catalog. Credentials are
// represented only as SecretReferences and are never retained in the Catalog.
func CatalogSourceFromConfig(cfg CatalogConfiguration) (CatalogSource, error) {
	prompts, err := bundledCatalogPrompts(cfg.PromptGroup)
	if err != nil {
		return CatalogSource{}, err
	}

	source := CatalogSource{
		ImageReference: cfg.ImageReference, PromptGroup: cfg.PromptGroup,
		Prompts: prompts,
		Workers: make([]CatalogWorker, 0, len(cfg.Workers)),
	}
	for _, configured := range cfg.Workers {
		adapter := string(configured.Type)
		definition, err := LookupAdapterDefinition(adapter)
		if err != nil {
			return CatalogSource{}, fmt.Errorf("worker %q: %w", configured.Name, err)
		}
		taskTypes, err := mapTaskTypes(configured.TaskTypes)
		if err != nil {
			return CatalogSource{}, fmt.Errorf("worker %q: %w", configured.Name, err)
		}
		worker := CatalogWorker{
			Name:         configured.Name,
			Adapter:      adapter,
			Model:        definition.model(configured.Env),
			MaxRunning:   configured.MaxRunning,
			Priority:     configured.Priority,
			Environment:  definition.catalogEnvironment(configured.Env),
			TaskTypes:    taskTypes,
			Capabilities: append([]string(nil), configured.Capabilities...),
		}
		for _, task := range worker.TaskTypes {
			worker.Capabilities = append(worker.Capabilities, string(task))
		}
		worker.SecretReferences, err = definition.secretReferences(configured)
		if err != nil {
			return CatalogSource{}, err
		}
		source.Workers = append(source.Workers, worker)
	}
	return source, nil
}

// ValidateCatalogConfiguration validates Catalog content independently of
// cfg.Revision, which is checked when constructing a revision-bound Catalog.
func ValidateCatalogConfiguration(cfg CatalogConfiguration) error {
	source, err := CatalogSourceFromConfig(cfg)
	if err != nil {
		return err
	}
	_, err = normalizeCatalog(source)
	return err
}

func NewCatalogFromConfig(cfg CatalogConfiguration) (*Catalog, error) {
	source, err := CatalogSourceFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Revision != "" {
		return NewCatalogForRevision(cfg.Revision, source)
	}
	return NewCatalog(source)
}

func mapTaskTypes(tasks []TaskType) ([]rundomain.TaskType, error) {
	result := make([]rundomain.TaskType, 0, len(tasks))
	for _, task := range tasks {
		switch task {
		case TaskTypeBootstrap:
			result = append(result, rundomain.TaskBootstrap)
		case TaskTypeReason:
			result = append(result, rundomain.TaskReason)
		case TaskTypeExplore:
			result = append(result, rundomain.TaskExplore)
		default:
			return nil, fmt.Errorf("unsupported task type %q", task)
		}
	}
	return result, nil
}
