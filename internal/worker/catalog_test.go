package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestCatalogRevisionIsDeterministicAndReadsAreImmutable(t *testing.T) {
	source := validCatalogSource()
	first, err := NewCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	source.Workers = []CatalogWorker{source.Workers[1], source.Workers[0]}
	second, err := NewCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision() == "" || first.Revision() != second.Revision() {
		t.Fatalf("Catalog revisions: first=%q second=%q", first.Revision(), second.Revision())
	}
	worker, ok := first.Worker("codex")
	if !ok {
		t.Fatal("Catalog worker missing")
	}
	worker.Environment["CODEX_MODEL"] = "mutated"
	worker.SecretReferences[0].Key = "mutated"
	again, _ := first.Worker("codex")
	if again.Environment["CODEX_MODEL"] != "gpt-5" || again.SecretReferences[0].Key != "CODEX_API_KEY" {
		t.Fatalf("Catalog exposed mutable state: %+v", again)
	}
	snapshot, err := first.WorkerSnapshot("codex")
	if err != nil || len(snapshot.SecretReferences) != 1 || snapshot.SecretReferences[0].Key != "CODEX_API_KEY" {
		t.Fatalf("Worker snapshot=%+v err=%v", snapshot, err)
	}
	adapter, err := first.AgentAdapter("codex")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := adapter.Prepare(AgentRequest{InvocationID: "12345678-abcd-4abc-8def-123456789abc",
		Task: rundomain.TaskReason, Phase: PhaseReason, Prompt: "reason",
		Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := prepared.Env()["OPENAI_API_KEY"]; leaked {
		t.Fatal("Catalog Agent Adapter contained a resolved Secret")
	}
	bootstrapHash, err := first.PromptTemplateHash(rundomain.TaskBootstrap)
	if err != nil || len(bootstrapHash) != 64 {
		t.Fatalf("Bootstrap prompt hash=%q err=%v", bootstrapHash, err)
	}
}

type alteredSubmissionIdentity struct {
	submissionProtocol
	contractSuffix string
	templatePhase  Phase
}

func (protocol alteredSubmissionIdentity) submissionIdentity(tasks ...rundomain.TaskType) submissionIdentity {
	identity := protocol.submissionProtocol.submissionIdentity(tasks...)
	identity.Contract += protocol.contractSuffix
	if template, ok := identity.Templates[protocol.templatePhase]; ok {
		identity.Templates[protocol.templatePhase] = template + "# changed static template\n"
	}
	return identity
}

func TestCatalogIdentityUsesDefinitionSubmissionMaterials(t *testing.T) {
	source := validCatalogSource()
	source.Workers[0].Adapter = "pi"
	source.Workers[0].Environment = map[string]string{"PI_BASE_URL": "https://example.invalid", "PI_PROVIDER_API": "openai-completions"}
	source.Workers[0].SecretReferences[0].EnvironmentVariable = "PI_API_KEY"
	baseline, err := NewCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	withoutPi, err := NewCatalog(validCatalogSource())
	if err != nil {
		t.Fatal(err)
	}
	tasks := []rundomain.TaskType{rundomain.TaskReason, rundomain.TaskExplore, rundomain.TaskBootstrap}
	baselineHashes := make(map[rundomain.TaskType]string)
	withoutPiHashes := make(map[rundomain.TaskType]string)
	for _, task := range tasks {
		baselineHashes[task], err = baseline.PromptTemplateHash(task)
		if err != nil {
			t.Fatal(err)
		}
		withoutPiHashes[task], err = withoutPi.PromptTemplateHash(task)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name           string
		contractSuffix string
		templatePhase  Phase
		affectedTask   rundomain.TaskType
	}{
		{name: "contract", contractSuffix: " changed static contract"},
		{name: "reason_finalization", templatePhase: PhaseReasonFinalization, affectedTask: rundomain.TaskReason},
		{name: "explore_recovery", templatePhase: PhaseExploreConcludeRecovery, affectedTask: rundomain.TaskExplore},
		{name: "bootstrap_recovery", templatePhase: PhaseBootstrapConcludeRecovery, affectedTask: rundomain.TaskBootstrap},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := adapterDefinitions["pi"]
			changed := original
			changed.submission = alteredSubmissionIdentity{
				submissionProtocol: original.submission, contractSuffix: test.contractSuffix, templatePhase: test.templatePhase,
			}
			adapterDefinitions["pi"] = changed
			t.Cleanup(func() { adapterDefinitions["pi"] = original })
			catalog, err := NewCatalog(source)
			if err != nil {
				t.Fatal(err)
			}
			if catalog.Revision() == baseline.Revision() {
				t.Fatal("static submission material did not change Catalog revision")
			}
			unaffected, err := NewCatalog(validCatalogSource())
			if err != nil {
				t.Fatal(err)
			}
			if unaffected.Revision() != withoutPi.Revision() {
				t.Fatal("an unconfigured adapter changed Catalog revision")
			}
			for _, task := range tasks {
				hash, err := catalog.PromptTemplateHash(task)
				if err != nil {
					t.Fatal(err)
				}
				wantChanged := test.contractSuffix != "" || task == test.affectedTask
				if (hash != baselineHashes[task]) != wantChanged {
					t.Fatalf("static submission identity change for %s: changed=%t want=%t", task, hash != baselineHashes[task], wantChanged)
				}
				unaffectedHash, err := unaffected.PromptTemplateHash(task)
				if err != nil || unaffectedHash != withoutPiHashes[task] {
					t.Fatalf("an unconfigured adapter changed %s prompt identity: hash=%q err=%v", task, unaffectedHash, err)
				}
			}
		})
	}
}

func TestSubmissionIdentityContainsEveryStaticPhaseTemplate(t *testing.T) {
	for _, name := range SupportedAdapterNames() {
		definition, err := LookupAdapterDefinition(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, test := range []struct {
			task   rundomain.TaskType
			phases []Phase
		}{
			{rundomain.TaskReason, []Phase{PhaseReason, PhaseReasonFinalization}},
			{rundomain.TaskExplore, []Phase{PhaseExplore, PhaseExploreConclude, PhaseExploreRecovery, PhaseExploreConcludeRecovery}},
			{rundomain.TaskBootstrap, []Phase{PhaseBootstrap, PhaseBootstrapConclude, PhaseBootstrapRecovery, PhaseBootstrapConcludeRecovery}},
		} {
			t.Run(name+"/"+string(test.task), func(t *testing.T) {
				identity := definition.submission.submissionIdentity(test.task)
				if identity.Contract == "" {
					t.Fatal("submission identity is missing its static contract")
				}
				if name != "pi" {
					if len(identity.Templates) != 0 {
						t.Fatal("JSON submission identity must exclude invocation schemas")
					}
					return
				}
				if len(identity.Templates) != len(test.phases) {
					t.Fatalf("static template count for %s: got %d want %d", test.task, len(identity.Templates), len(test.phases))
				}
				for _, phase := range test.phases {
					if !strings.Contains(identity.Templates[phase], `kind = ""`) {
						t.Fatalf("missing static file template for %s", phase)
					}
				}
			})
		}
	}
}

func TestCatalogIdentityBindsFinalizationAndPiFileContract(t *testing.T) {
	source := validCatalogSource()
	source.Workers[0].Adapter = "pi"
	source.Workers[0].Environment = map[string]string{"PI_BASE_URL": "https://example.invalid", "PI_PROVIDER_API": "openai-completions"}
	source.Workers[0].SecretReferences[0].EnvironmentVariable = "PI_API_KEY"
	baseline, err := NewCatalog(source)
	if err != nil {
		t.Fatal(err)
	}

	for _, task := range []rundomain.TaskType{
		rundomain.TaskReason, rundomain.TaskExplore, rundomain.TaskBootstrap,
	} {
		t.Run(string(task), func(t *testing.T) {
			baselineHash, err := baseline.PromptTemplateHash(task)
			if err != nil {
				t.Fatal(err)
			}

			changedFinalizationSource := validCatalogSource()
			changedFinalizationSource.Workers[0] = source.Workers[0]
			prompt := changedFinalizationSource.Prompts[task]
			prompt.Finalization += " changed"
			changedFinalizationSource.Prompts[task] = prompt
			changedFinalization, err := NewCatalog(changedFinalizationSource)
			if err != nil {
				t.Fatal(err)
			}
			changedFinalizationHash, err := changedFinalization.PromptTemplateHash(task)
			if err != nil {
				t.Fatal(err)
			}
			if baseline.Revision() == changedFinalization.Revision() || baselineHash == changedFinalizationHash {
				t.Fatal("Finalization template did not change Catalog revision and Prompt Template Hash")
			}

		})
	}

	adapter, err := baseline.AgentAdapter("codex")
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := adapter.Prepare(AgentRequest{
		InvocationID: "12345678-abcd-4abc-8def-123456789abc",
		Task:         rundomain.TaskExplore, Phase: PhaseExplore, Prompt: "explore",
		Input: InputReference{Kind: InputBlackboardFile, Path: testBlackboardPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(invocation.Argv(), "\n"), "Pi Result File Contract") || invocation.ProtocolFile() == nil {
		t.Fatal("Catalog Adapter did not execute the Pi file contract bound into Prompt identity")
	}
}

func TestCatalogPublisherKeepsPreviousRevisionAfterInvalidReload(t *testing.T) {
	publisher := &CatalogPublisher{}
	published, err := publisher.Publish(validCatalogSource())
	if err != nil {
		t.Fatal(err)
	}
	_, err = publisher.Reload(func() (CatalogSource, error) {
		return CatalogSource{}, errors.New("invalid hot reload")
	})
	if err == nil || publisher.Current() != published || publisher.Current().Revision() != published.Revision() {
		t.Fatalf("failed reload replaced active Catalog: current=%p published=%p err=%v", publisher.Current(), published, err)
	}
	invalid := validCatalogSource()
	invalid.Workers[0].Environment["OPENAI_API_KEY"] = "must-not-enter-catalog"
	if _, err := publisher.Publish(invalid); err == nil || publisher.Current() != published {
		t.Fatalf("invalid Catalog was published: current=%p published=%p err=%v", publisher.Current(), published, err)
	}
}

func TestCatalogRequiresSecretReferencesAndPreservesOrdinaryEnvironment(t *testing.T) {
	source := validCatalogSource()
	source.Workers[0].SecretReferences = nil
	if _, err := NewCatalog(source); err == nil {
		t.Fatal("worker without required Secret Reference was accepted")
	}
	source = validCatalogSource()
	source.Workers[0].Environment["OPENAI_API_KEY"] = "secret-value"
	if _, err := NewCatalog(source); err == nil {
		t.Fatal("worker environment containing a Secret value was accepted")
	}
	source = validCatalogSource()
	source.Workers[0].Environment["AWS_SECRET_ACCESS_KEY"] = "ordinary-worker-configuration"
	catalog, err := NewCatalog(source)
	if err != nil {
		t.Fatalf("Catalog rejected an ordinary Worker environment variable: %v", err)
	}
	worker, ok := catalog.Worker("codex")
	if !ok || worker.Environment["AWS_SECRET_ACCESS_KEY"] != "ordinary-worker-configuration" {
		t.Fatalf("ordinary Worker environment variable was not retained: %+v", worker.Environment)
	}
	source = validCatalogSource()
	source.Workers[0].SecretReferences = append(source.Workers[0].SecretReferences, rundomain.SecretReference{
		EnvironmentVariable: "PATH", Provider: "configuration", Key: "MALICIOUS_PATH",
	})
	if _, err := NewCatalog(source); err == nil {
		t.Fatal("Secret Reference targeting a controlled runtime variable was accepted")
	}
}

func TestCatalogBindsSnapshotModelToAdapterConfiguration(t *testing.T) {
	source := validCatalogSource()
	source.Workers[0].Environment["CODEX_MODEL"] = "different-model"
	if _, err := NewCatalog(source); err == nil {
		t.Fatal("Catalog accepted a model different from the immutable Worker snapshot")
	}
	source = validCatalogSource()
	delete(source.Workers[0].Environment, "CODEX_BASE_URL")
	if _, err := NewCatalog(source); err == nil {
		t.Fatal("Catalog accepted a Worker without its required Base URL")
	}
	source = validCatalogSource()
	source.Workers[0].Environment["CODEX_BASE_URL"] = "not-a-url"
	if _, err := NewCatalog(source); err == nil {
		t.Fatal("Catalog accepted an invalid Adapter Base URL")
	}
	for _, baseURL := range []string{
		"https://user:secret@api.example.invalid/v1",
		"https://api.example.invalid/v1?token=secret",
		"https://api.example.invalid/v1#secret",
	} {
		source = validCatalogSource()
		source.Workers[0].Environment["CODEX_BASE_URL"] = baseURL
		if _, err := NewCatalog(source); err == nil {
			t.Fatalf("Catalog accepted credential-bearing Adapter Base URL %q", baseURL)
		}
	}
	source = validCatalogSource()
	source.Workers[1].Environment = map[string]string{"MOCK_REASON": "not-json"}
	if _, err := NewCatalog(source); err == nil {
		t.Fatal("Catalog accepted invalid Mock Adapter configuration")
	}
}

func TestCatalogRejectsNonCanonicalSecretReferences(t *testing.T) {
	for name, mutate := range map[string]func(*rundomain.SecretReference){
		"provider whitespace": func(reference *rundomain.SecretReference) { reference.Provider = " environment" },
		"provider null":       func(reference *rundomain.SecretReference) { reference.Provider = "environment\x00" },
		"key whitespace":      func(reference *rundomain.SecretReference) { reference.Key = " CODEX_API_KEY" },
		"key null":            func(reference *rundomain.SecretReference) { reference.Key = "CODEX_API_KEY\x00" },
	} {
		t.Run(name, func(t *testing.T) {
			source := validCatalogSource()
			mutate(&source.Workers[0].SecretReferences[0])
			if _, err := NewCatalog(source); err == nil {
				t.Fatal("Catalog accepted a non-canonical Secret Reference")
			}
		})
	}
}

func TestProviderReadinessRefreshPublishesHealthcheckResults(t *testing.T) {
	catalog, err := NewCatalog(validCatalogSource())
	if err != nil {
		t.Fatal(err)
	}
	readiness := NewProviderReadiness()
	failing := ProviderHealthCheckerFunc(func(_ context.Context, worker CatalogWorker) error {
		if worker.Name == "codex" {
			return errors.New("provider unavailable")
		}
		return nil
	})
	if err := readiness.Refresh(t.Context(), catalog, failing); err == nil {
		t.Fatal("Provider readiness refresh lost a Worker failure")
	}
	request := rundomain.StartRequest{Spec: rundomain.AdmissionSpecSnapshot{
		ConfigRevision: catalog.Revision(), Worker: rundomain.WorkerSnapshot{Name: "codex"},
	}}
	if err := readiness.CheckStartReadiness(t.Context(), request); err == nil {
		t.Fatal("failed Provider healthcheck was marked ready")
	}
	if err := readiness.Refresh(t.Context(), catalog, ProviderHealthCheckerFunc(
		func(context.Context, CatalogWorker) error { return nil },
	)); err != nil {
		t.Fatal(err)
	}
	if err := readiness.CheckStartReadiness(t.Context(), request); err != nil {
		t.Fatalf("healthy Provider was not marked ready: %v", err)
	}
}

func TestProviderReadinessRefreshChecksWorkersConcurrently(t *testing.T) {
	catalog, err := NewCatalog(validCatalogSource())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan string, len(catalog.Workers()))
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- NewProviderReadiness().Refresh(t.Context(), catalog, ProviderHealthCheckerFunc(
			func(_ context.Context, configured CatalogWorker) error {
				started <- configured.Name
				<-release
				return nil
			},
		))
	}()
	for range catalog.Workers() {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("Provider healthchecks did not run concurrently")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProviderReadinessRefreshDoesNotAllowOldEpochToOverwriteNewResults(t *testing.T) {
	catalog, err := NewCatalog(validCatalogSource())
	if err != nil {
		t.Fatal(err)
	}
	readiness := NewProviderReadiness()
	started := make(chan struct{})
	release := make(chan struct{})
	old := ProviderHealthCheckerFunc(func(_ context.Context, worker CatalogWorker) error {
		if worker.Name == "codex" {
			close(started)
			<-release
		}
		return nil
	})
	oldDone := make(chan error, 1)
	go func() { oldDone <- readiness.Refresh(t.Context(), catalog, old) }()
	<-started
	if err := readiness.Refresh(t.Context(), catalog, ProviderHealthCheckerFunc(func(context.Context, CatalogWorker) error { return nil })); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-oldDone; err == nil {
		t.Fatal("superseded Provider refresh reported success")
	}
}

func TestProviderReadinessManualUpdateSupersedesRefresh(t *testing.T) {
	catalog, err := NewCatalog(validCatalogSource())
	if err != nil {
		t.Fatal(err)
	}
	readiness := NewProviderReadiness()
	started := make(chan struct{})
	release := make(chan struct{})
	checker := ProviderHealthCheckerFunc(func(_ context.Context, worker CatalogWorker) error {
		if worker.Name == "codex" {
			close(started)
			<-release
			return errors.New("stale provider failure")
		}
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- readiness.Refresh(t.Context(), catalog, checker) }()
	<-started
	readiness.MarkReady(catalog.Revision(), "codex")
	close(release)
	if err := <-done; err == nil {
		t.Fatal("superseded Provider refresh reported success")
	}
	request := rundomain.StartRequest{Spec: rundomain.AdmissionSpecSnapshot{
		ConfigRevision: catalog.Revision(), Worker: rundomain.WorkerSnapshot{Name: "codex"},
	}}
	if err := readiness.CheckStartReadiness(t.Context(), request); err != nil {
		t.Fatalf("stale refresh overwrote a newer Provider state: %v", err)
	}
}

func TestProviderReadinessRetiresPreviousConfigRevision(t *testing.T) {
	oldCatalog, err := NewCatalog(validCatalogSource())
	if err != nil {
		t.Fatal(err)
	}
	readiness := NewProviderReadiness()
	readiness.MarkReady(oldCatalog.Revision(), "codex")
	source := validCatalogSource()
	prompt := source.Prompts[rundomain.TaskReason]
	prompt.Primary = "new reason prompt"
	source.Prompts[rundomain.TaskReason] = prompt
	newCatalog, err := NewCatalog(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := readiness.Refresh(t.Context(), newCatalog, ProviderHealthCheckerFunc(
		func(context.Context, CatalogWorker) error { return nil },
	)); err != nil {
		t.Fatal(err)
	}
	oldRequest := rundomain.StartRequest{Spec: rundomain.AdmissionSpecSnapshot{
		ConfigRevision: oldCatalog.Revision(), Worker: rundomain.WorkerSnapshot{Name: "codex"},
	}}
	if err := readiness.CheckStartReadiness(t.Context(), oldRequest); err == nil {
		t.Fatal("Provider readiness retained a retired Config Revision")
	}
	newRequest := oldRequest
	newRequest.Spec.ConfigRevision = newCatalog.Revision()
	if err := readiness.CheckStartReadiness(t.Context(), newRequest); err != nil {
		t.Fatalf("new Config Revision was not published: %v", err)
	}
}

func validCatalogSource() CatalogSource {
	return CatalogSource{
		ImageReference: "registry.example/aether-agent@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Workers: []CatalogWorker{
			{
				Name: "codex", Adapter: "codex", Model: "gpt-5", TaskTypes: []rundomain.TaskType{rundomain.TaskExplore, rundomain.TaskReason},
				MaxRunning: 2, Environment: map[string]string{"CODEX_BASE_URL": "https://api.example.invalid"},
				Capabilities: []string{"reason", "explore"}, SecretReferences: []rundomain.SecretReference{{
					EnvironmentVariable: "OPENAI_API_KEY", Provider: "configuration", Key: "CODEX_API_KEY",
				}},
			},
			{Name: "mock", Adapter: "mock", TaskTypes: []rundomain.TaskType{rundomain.TaskBootstrap}, MaxRunning: 1},
		},
		Prompts: map[rundomain.TaskType]PromptSet{
			rundomain.TaskReason:    {Primary: "reason", Finalization: "finalize reason"},
			rundomain.TaskExplore:   {Primary: "explore", Fallback: "explore fallback", Finalization: "finalize explore"},
			rundomain.TaskBootstrap: {Primary: "bootstrap", Fallback: "bootstrap fallback", Finalization: "finalize bootstrap"},
		},
	}
}
