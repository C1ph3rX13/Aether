package config_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/database"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

func newConfigurationService(t *testing.T, healthcheck config.ProviderHealthcheckFunc) (*database.DB, *config.Service) {
	t.Helper()
	db := database.NewDB()
	if err := db.Configure(filepath.Join(t.TempDir(), "aether.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := config.InitializeDeployment(t.Context(), db, "deployment-one"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	service, err := config.NewService(db, config.ServiceOptions{OperatorName: "test-operator", Healthcheck: healthcheck})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, service
}

func completeDraft(t *testing.T) config.DynamicDraft {
	t.Helper()
	draft := config.RecommendedDynamicDraft()
	draft.Workers = []config.DynamicWorkerDraft{{
		Name: "codex-deepseek", AgentType: "codex", ProviderName: "DeepSeek",
		TaskTypes: []string{"bootstrap", "reason", "explore"}, MaxRunning: 2,
		Priority: 1, AgentEnv: map[string]string{
			"CODEX_BASE_URL": "https://api.deepseek.com", "CODEX_MODEL": "deepseek-v4-flash",
			"CODEX_REASONING_EFFORT": "high",
		}, TimeoutOverrides: map[string]int{"reason.primary": 500},
	}}
	return draft
}

func saveCompleteWorker(t *testing.T, service *config.Service, secret string) *config.ConfigurationRead {
	t.Helper()
	read, err := service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: 1,
		Draft:                completeDraft(t),
		Secrets: map[string]map[string]config.WorkerSecretChange{
			"codex-deepseek": {"OPENAI_API_KEY": {Value: secret}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return read
}

func TestConfigurationSaveImmediatelyPublishesEffectiveRevision(t *testing.T) {
	db, service := newConfigurationService(t, func(context.Context, worker.CatalogWorker, map[string]string) error { return nil })
	defer db.Close()
	saved := saveCompleteWorker(t, service, "secret-one")
	active, err := service.ReadActiveRevision(t.Context())
	if err != nil {
		t.Fatalf("saved configuration is not effective: %v", err)
	}
	if saved.ActiveRevision == nil || active.ID != saved.ActiveRevision.ID || active.Configuration.Workers[0].Name != "codex-deepseek" {
		t.Fatal("save did not publish Worker")
	}
	deleted, err := service.DeleteWorker(t.Context(), "codex-deepseek", config.DynamicWorkerDeleteRequest{ExpectedDraftVersion: saved.DraftVersion})
	if err != nil {
		t.Fatal(err)
	}
	active, err = service.ReadActiveRevision(t.Context())
	if err != nil || len(active.Configuration.Workers) != 0 || deleted.ActiveRevision.ID != active.ID {
		t.Fatalf("deleted Worker still effective: %v", err)
	}
	if len(deleted.ValidationProblems) != 3 {
		t.Fatalf("missing task coverage not reported: %+v", deleted.ValidationProblems)
	}
	historical, err := service.ReadRevision(t.Context(), saved.ActiveRevision.ID)
	if err != nil || len(historical.Configuration.Workers) != 1 {
		t.Fatal("deletion changed historical Worker")
	}
}

func TestDynamicConfigurationSaveAndSecretLifecycle(t *testing.T) {
	db, service := newConfigurationService(t, func(_ context.Context, _ worker.CatalogWorker, _ map[string]string) error { return nil })
	defer db.Close()

	read := saveCompleteWorker(t, service, "secret-one")
	if read.DraftVersion != 2 || !read.Draft.Workers[0].Secrets["OPENAI_API_KEY"].Configured {
		t.Fatalf("saved configuration = %+v", read)
	}
	encoded, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-one") || strings.Contains(string(encoded), "secret-two") {
		t.Fatalf("configuration response leaked a Secret: %s", encoded)
	}

	retained := read.Draft
	retained.Runtime.MaxWorkers++
	read, err = service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: 2, Draft: retained,
	})
	if err != nil {
		t.Fatal(err)
	}
	if read.DraftVersion != 3 || !read.Draft.Workers[0].Secrets["OPENAI_API_KEY"].Configured {
		t.Fatalf("retained Secret was lost: %+v", read.Draft.Workers[0])
	}

	replaced := read.Draft
	read, err = service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: 3, Draft: replaced,
		Secrets: map[string]map[string]config.WorkerSecretChange{
			"codex-deepseek": {"OPENAI_API_KEY": {Value: "secret-two"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !read.Draft.Workers[0].Secrets["OPENAI_API_KEY"].Configured {
		t.Fatal("replacement removed the Secret")
	}

	before := read.ActiveRevision.ID
	if _, err := service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{ExpectedDraftVersion: 4, Draft: read.Draft, Secrets: map[string]map[string]config.WorkerSecretChange{"codex-deepseek": {"OPENAI_API_KEY": {Clear: true}}}}); err == nil {
		t.Fatal("missing required Secret was saved")
	}
	active, err := service.ReadActiveRevision(t.Context())
	if err != nil || active.ID != before {
		t.Fatal("failed Secret clear changed effective configuration")
	}

}

func TestDynamicConfigurationDeleteWorkerUsesDraftCAS(t *testing.T) {
	db, service := newConfigurationService(t, func(_ context.Context, _ worker.CatalogWorker, _ map[string]string) error { return nil })
	defer db.Close()

	read := saveCompleteWorker(t, service, "secret-one")
	if _, err := service.DeleteWorker(t.Context(), "codex-deepseek", config.DynamicWorkerDeleteRequest{ExpectedDraftVersion: read.DraftVersion - 1}); err == nil {
		t.Fatal("stale Worker deletion was accepted")
	} else {
		var domainErr *config.DomainError
		if !errors.As(err, &domainErr) || domainErr.Code != config.ErrorConflict {
			t.Fatalf("stale Worker deletion error = %v", err)
		}
	}

	read, err := service.DeleteWorker(t.Context(), "codex-deepseek", config.DynamicWorkerDeleteRequest{ExpectedDraftVersion: read.DraftVersion})
	if err != nil {
		t.Fatal(err)
	}
	if read.DraftVersion != 3 || len(read.Draft.Workers) != 0 {
		t.Fatalf("deleted Worker remained in draft: %+v", read)
	}
	if _, err := service.DeleteWorker(t.Context(), "codex-deepseek", config.DynamicWorkerDeleteRequest{ExpectedDraftVersion: read.DraftVersion}); err == nil {
		t.Fatal("deleting an unknown Worker was accepted")
	} else {
		var domainErr *config.DomainError
		if !errors.As(err, &domainErr) || domainErr.Code != config.ErrorNotFound {
			t.Fatalf("unknown Worker deletion error = %v", err)
		}
	}
}

func TestDispatchConfigResolvesWorkerTaskExecutionPolicy(t *testing.T) {
	db, service := newConfigurationService(t, func(_ context.Context, _ worker.CatalogWorker, _ map[string]string) error { return nil })
	defer db.Close()

	draft := completeDraft(t)
	draft.Workers[0].TimeoutOverrides = map[string]int{
		"reason.primary":    12,
		"explore.primary":   40,
		"explore.conclude":  8,
		"explore.recovery":  2,
		"bootstrap.primary": 50,
	}
	_, err := service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: 1,
		Draft:                draft,
		Secrets: map[string]map[string]config.WorkerSecretChange{
			"codex-deepseek": {"OPENAI_API_KEY": {Value: "secret-one"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := service.ReadActiveRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := config.DispatchConfigFromRevision(*revision)
	if err != nil {
		t.Fatal(err)
	}
	policies := dispatch.ExecutionPolicies["codex-deepseek"]
	explore := policies[rundomain.TaskExplore]
	if explore.Timeouts != (rundomain.TimeoutSnapshot{RunSeconds: 66, PhaseSeconds: 40, FallbackPhaseSeconds: 8, RecoveryPhaseSeconds: 2, GraceSeconds: 2}) {
		t.Fatalf("resolved Explore policy = %+v", explore)
	}
	reason := policies[rundomain.TaskReason]
	if reason.Timeouts != (rundomain.TimeoutSnapshot{RunSeconds: 48, PhaseSeconds: 12, GraceSeconds: 12}) || reason.MaxProposedIntents != 2 {
		t.Fatalf("resolved Reason policy = %+v", reason)
	}
	if reason.RetryPolicy.MaxAttempts != 3 || reason.RetryPolicy.InitialBackoff != 30*time.Second ||
		reason.RetryPolicy.MaxBackoff != 5*time.Minute || reason.RetryPolicy.Multiplier != 2 ||
		reason.RetryPolicy.DeclineCooldown != 5*time.Second {
		t.Fatalf("resolved Retry policy = %+v", reason.RetryPolicy)
	}
}

func TestDynamicConfigurationUsesRunExecutionBounds(t *testing.T) {
	db, service := newConfigurationService(t, func(_ context.Context, _ worker.CatalogWorker, _ map[string]string) error { return nil })
	defer db.Close()

	draft := completeDraft(t)
	draft.Tasks.Reason.MaxIntents = rundomain.MaximumReasonProposedIntents
	draft.Retry.Reason.MaxAttempts = rundomain.MaximumRetryAttempts
	read, err := service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: 1,
		Draft:                draft,
		Secrets: map[string]map[string]config.WorkerSecretChange{
			"codex-deepseek": {"OPENAI_API_KEY": {Value: "secret-one"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertValidation := func(candidate config.DynamicDraft, path string) {
		t.Helper()
		_, validationErr := service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{ExpectedDraftVersion: read.DraftVersion, Draft: candidate})
		var domainErr *config.DomainError
		if !errors.As(validationErr, &domainErr) || domainErr.Code != config.ErrorValidation ||
			!slices.ContainsFunc(domainErr.ValidationProblems, func(problem config.ValidationProblem) bool { return problem.Path == path }) {
			t.Fatalf("invalid execution bound error = %v", validationErr)
		}
	}
	tooManyRetries := read.Draft
	tooManyRetries.Retry.Reason.MaxAttempts = rundomain.MaximumRetryAttempts + 1
	assertValidation(tooManyRetries, "retry.reason.max_attempts")
	tooManyIntents := read.Draft
	tooManyIntents.Tasks.Reason.MaxIntents = rundomain.MaximumReasonProposedIntents + 1
	assertValidation(tooManyIntents, "tasks.reason.max_intents")
}

func TestDynamicConfigurationCASAndProviderFailureAreAtomic(t *testing.T) {
	var calls int
	db, service := newConfigurationService(t, func(_ context.Context, _ worker.CatalogWorker, _ map[string]string) error {
		calls++
		return errors.New("provider authentication failed: secret-one")
	})
	defer db.Close()

	if _, err := service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: 1, Draft: completeDraft(t),
		Secrets: map[string]map[string]config.WorkerSecretChange{
			"codex-deepseek": {"OPENAI_API_KEY": {Value: "secret-one"}},
		},
	}); err == nil {
		t.Fatal("Provider failure was accepted")
	} else if strings.Contains(err.Error(), "secret-one") {
		t.Fatalf("Provider diagnostic leaked the candidate Secret: %v", err)
	}
	if calls != 1 {
		t.Fatalf("healthcheck calls = %d, want 1", calls)
	}
	read, err := service.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if read.DraftVersion != 1 || len(read.Draft.Workers) != 0 {
		t.Fatalf("failed save changed Draft: %+v", read)
	}
	var secretCount int
	conn, err := db.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM worker_secret_versions").Scan(&secretCount); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if secretCount != 0 {
		t.Fatalf("failed save left %d Secret Versions", secretCount)
	}

	goodDB, good := newConfigurationService(t, func(_ context.Context, _ worker.CatalogWorker, _ map[string]string) error { return nil })
	defer goodDB.Close()
	var resultsMu sync.Mutex
	var successes, conflicts int
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := good.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{
				ExpectedDraftVersion: 1, Draft: completeDraft(t),
				Secrets: map[string]map[string]config.WorkerSecretChange{
					"codex-deepseek": {"OPENAI_API_KEY": {Value: "secret-one"}},
				},
			})
			if err == nil {
				resultsMu.Lock()
				successes++
				resultsMu.Unlock()
				return
			}
			var domainErr *config.DomainError
			if errors.As(err, &domainErr) && domainErr.Code == config.ErrorConflict {
				resultsMu.Lock()
				conflicts++
				resultsMu.Unlock()
			}
		}()
	}
	wait.Wait()
	if successes != 1 || conflicts != 1 {
		t.Fatalf("CAS results successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestWorkerSecretReplacementRequiresProviderCheck(t *testing.T) {
	failCheck := false
	var calls int
	db, service := newConfigurationService(t, func(_ context.Context, _ worker.CatalogWorker, _ map[string]string) error {
		calls++
		if failCheck {
			return errors.New("Provider unavailable")
		}
		return nil
	})
	defer db.Close()
	read := saveCompleteWorker(t, service, "previous-credential")
	var err error
	var request config.DynamicDraftSaveRequest
	before, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	calls = 0
	failCheck = true
	request = config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: read.DraftVersion, Draft: read.Draft,
		Secrets: map[string]map[string]config.WorkerSecretChange{"codex-deepseek": {"OPENAI_API_KEY": {Value: "new-credential"}}},
	}
	if _, err := service.SaveDraft(t.Context(), request); err == nil {
		t.Fatal("adding a Secret bypassed the failing Provider check")
	}
	after, err := service.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(before) {
		t.Fatal("failed Secret setup changed saved configuration")
	}
	failCheck = false
	after, err = service.SaveDraft(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Draft.Workers[0].Secrets["OPENAI_API_KEY"].Configured || after.DraftVersion != read.DraftVersion+1 || calls != 2 {
		t.Fatal("Secret setup was not checked and committed atomically")
	}
}

func TestDynamicConfigurationRejectsExecEnvironmentOverflowButPreservesCustomValues(t *testing.T) {
	db, service := newConfigurationService(t, func(_ context.Context, _ worker.CatalogWorker, _ map[string]string) error { return nil })
	defer db.Close()

	draft := config.RecommendedDynamicDraft()
	draft.Workers = []config.DynamicWorkerDraft{{
		Name: "custom", AgentType: "codex", TaskTypes: []string{"reason"}, MaxRunning: 1,
		AgentEnv: map[string]string{
			"CODEX_BASE_URL": "https://api.example.invalid", "CODEX_MODEL": "model",
			"CUSTOM_VALUE": strings.Repeat("x", 256*1024),
		},
	}}
	if _, err := service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{ExpectedDraftVersion: 1, Draft: draft, Secrets: map[string]map[string]config.WorkerSecretChange{"custom": {"OPENAI_API_KEY": {Value: "fixture-secret"}}}}); err == nil {
		t.Fatal("environment overflow was accepted")
	}

	draft.Workers[0].AgentEnv["CUSTOM_VALUE"] = strings.Repeat("x", 16*1024)
	read, err := service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{ExpectedDraftVersion: 1, Draft: draft, Secrets: map[string]map[string]config.WorkerSecretChange{"custom": {"OPENAI_API_KEY": {Value: "fixture-secret"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if read.Draft.Workers[0].AgentEnv["CUSTOM_VALUE"] != strings.Repeat("x", 16*1024) {
		t.Fatal("custom environment value was changed")
	}

	draft.Workers[0].AgentEnv["CUSTOM_ENDPOINT"] = "not a URL"
	read, err = service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{ExpectedDraftVersion: 2, Draft: draft, Secrets: map[string]map[string]config.WorkerSecretChange{"custom": {"OPENAI_API_KEY": {Value: "fixture-secret"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if read.Draft.Workers[0].AgentEnv["CUSTOM_ENDPOINT"] != "not a URL" {
		t.Fatal("custom environment value was interpreted or changed")
	}
}

func TestDynamicConfigurationOutputLimitsAreExplicitAndBounded(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   int64
		wantErr bool
	}{
		{name: "minimum", value: config.MinOutputLimitBytes},
		{name: "maximum", value: config.MaxOutputLimitBytes},
		{name: "below minimum", value: config.MinOutputLimitBytes - 1, wantErr: true},
		{name: "above maximum", value: config.MaxOutputLimitBytes + 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, service := newConfigurationService(t, func(_ context.Context, _ worker.CatalogWorker, _ map[string]string) error { return nil })
			defer db.Close()
			read, err := service.Read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if read.Draft.Container.StdoutLimitBytes != config.DefaultOutputLimitBytes ||
				read.Draft.Container.StderrLimitBytes != config.DefaultOutputLimitBytes {
				t.Fatalf("recommended output limits = %d/%d", read.Draft.Container.StdoutLimitBytes, read.Draft.Container.StderrLimitBytes)
			}
			draft := read.Draft
			draft.Container.StdoutLimitBytes = test.value
			draft.Container.StderrLimitBytes = test.value
			_, err = service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{
				ExpectedDraftVersion: read.DraftVersion,
				Draft:                draft,
			})
			if (err != nil) != test.wantErr {
				t.Fatalf("SaveDraft(output limit %d) error = %v, wantErr = %t", test.value, err, test.wantErr)
			}
		})
	}
}

func TestDynamicConfigurationMetadataIsComplete(t *testing.T) {
	db, service := newConfigurationService(t, func(_ context.Context, _ worker.CatalogWorker, _ map[string]string) error { return nil })
	defer db.Close()
	read, err := service.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(read.AgentDefinitions) != 3 || len(read.ProviderPresets) != 3 {
		t.Fatalf("metadata definitions=%d presets=%d", len(read.AgentDefinitions), len(read.ProviderPresets))
	}
	if got, want := strings.Join(read.PromptGroups, ","), "default,mock"; got != want {
		t.Fatalf("Prompt Groups = %q, want %q", got, want)
	}
	modelFields := map[string]string{
		"codex": "CODEX_MODEL", "claude_code": "ANTHROPIC_MODEL", "pi": "PI_MODEL",
	}
	for _, preset := range read.ProviderPresets {
		if field, ok := modelFields[preset.AgentType]; !ok || preset.AgentEnv[field] != "deepseek-flash" {
			t.Fatalf("DeepSeek preset model for %s = %+v, want deepseek-flash", preset.AgentType, preset.AgentEnv)
		}
		if preset.AgentType == "codex" && (preset.Protocol != "openai-responses" || preset.AgentEnv["CODEX_BASE_URL"] != "https://api.deepseek.com") {
			t.Fatalf("Codex preset = %+v", preset)
		}
		for _, value := range preset.AgentEnv {
			if value == "secret-one" {
				t.Fatal("Preset contains a Secret")
			}
		}
	}
}

func TestDynamicConfigurationSaveCreatesAndReusesImmutableRevision(t *testing.T) {
	var healthchecks int
	db, service := newConfigurationService(t, func(_ context.Context, _ worker.CatalogWorker, _ map[string]string) error {
		healthchecks++
		return nil
	})
	defer db.Close()

	saveCompleteWorker(t, service, "secret-one")
	if healthchecks != 1 {
		t.Fatalf("save healthchecks = %d, want 1", healthchecks)
	}

	revision, err := service.ReadActiveRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if healthchecks != 1 {
		t.Fatalf("Revision read performed external Provider I/O: healthchecks = %d", healthchecks)
	}
	if !strings.HasPrefix(revision.ID, "sha256:") || len(revision.ID) != len("sha256:")+sha256.Size*2 {
		t.Fatalf("Revision ID = %q", revision.ID)
	}
	secret := revision.Configuration.Workers[0].Secrets["OPENAI_API_KEY"]
	if !secret.Configured || secret.Mask != "********" || len(revision.SecretReferences) != 1 || revision.SecretReferences[0].VersionID == "" {
		t.Fatalf("public Revision Secret state=%+v references=%+v", secret, revision.SecretReferences)
	}
	encoded, err := json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-one") {
		t.Fatalf("public Revision leaked Secret plaintext: %s", encoded)
	}

	document, createdBy := readConfigurationRevisionRecord(t, db, revision.ID)
	digest := sha256.Sum256([]byte(document))
	if revision.ID != "sha256:"+hex.EncodeToString(digest[:]) || strings.Contains(document, "secret-one") {
		t.Fatalf("stored Revision identity/document mismatch: id=%q document=%s", revision.ID, document)
	}
	if createdBy != "test-operator" {
		t.Fatalf("Revision creator = %q", createdBy)
	}

	read, err := service.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{ExpectedDraftVersion: read.DraftVersion, Draft: read.Draft}); err != nil {
		t.Fatal(err)
	}

	reused, err := service.ReadActiveRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if reused.ID != revision.ID {
		t.Fatalf("equivalent activation ID = %q, want %q", reused.ID, revision.ID)
	}
	assertConfigurationRevisionCount(t, db, 1)

	active, err := service.ReadActiveRevision(t.Context())
	if err != nil || active.ID != revision.ID {
		t.Fatalf("active Revision = %+v, error = %v", active, err)
	}
	historical, err := service.ReadRevision(t.Context(), revision.ID)
	if err != nil || historical.ID != revision.ID {
		t.Fatalf("historical Revision = %+v, error = %v", historical, err)
	}

	if _, err := config.DispatchConfigFromRevision(*revision); err != nil {
		t.Fatalf("DispatchConfigFromRevision(valid revision): %v", err)
	}
	tampered := *revision
	tampered.Configuration.Runtime.SchedulerIntervalSeconds++
	if _, err := config.DispatchConfigFromRevision(tampered); err == nil {
		t.Fatal("DispatchConfigFromRevision accepted a Revision with mismatched contents")
	}

	if err := execConfigurationSQL(t, db, "UPDATE configuration_revisions SET created_by = 'other' WHERE id = ?", revision.ID); err == nil {
		t.Fatal("immutable Config Revision was updated")
	}
	if err := execConfigurationSQL(t, db, "DELETE FROM configuration_revisions WHERE id = ?", revision.ID); err == nil {
		t.Fatal("immutable Config Revision was deleted")
	}
	if err := execConfigurationSQL(t, db, "UPDATE worker_secret_versions SET secret_value = 'other'"); err == nil {
		t.Fatal("immutable Worker Secret Version was updated")
	}
	if err := execConfigurationSQL(t, db, "DELETE FROM worker_secret_versions"); err == nil {
		t.Fatal("immutable Worker Secret Version was deleted")
	}
}

func TestConfigurationSaveRejectsInvalidFieldsWithoutPublishing(t *testing.T) {
	db, service := newConfigurationService(t, func(context.Context, worker.CatalogWorker, map[string]string) error { return nil })
	defer db.Close()
	read := saveCompleteWorker(t, service, "secret-one")
	if _, err := service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{ExpectedDraftVersion: 1, Draft: read.Draft}); err == nil {
		t.Fatal("stale save succeeded")
	}
	invalid := read.Draft
	invalid.Runtime.MaxWorkers = 0
	invalid.Runtime.PromptGroup = ""
	invalid.Tasks.Explore.ConcludeSeconds = 0
	invalid.Retry.Reason.MaxAttempts = 0
	invalid.Container.CompletedAction = ""
	invalid.Container.StdoutLimitBytes = 0
	delete(invalid.Workers[0].AgentEnv, "CODEX_MODEL")
	_, err := service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{ExpectedDraftVersion: 2, Draft: invalid, Secrets: map[string]map[string]config.WorkerSecretChange{"codex-deepseek": {"OPENAI_API_KEY": {Clear: true}}}})
	var domainErr *config.DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != config.ErrorValidation {
		t.Fatalf("invalid save error: %v", err)
	}
	for _, path := range []string{"runtime.max_workers", "runtime.prompt_group", "tasks.explore.conclude_seconds", "retry.reason.max_attempts", "container.completed_action", "container.stdout_limit_bytes", "workers[0].agent_env.CODEX_MODEL", "workers[0].secrets.OPENAI_API_KEY"} {
		if !slices.ContainsFunc(domainErr.ValidationProblems, func(p config.ValidationProblem) bool { return p.Path == path }) {
			t.Errorf("missing validation: %s", path)
		}
	}
	after, err := service.Read(t.Context())
	if err != nil || after.DraftVersion != 2 || after.ActiveRevision.ID != read.ActiveRevision.ID {
		t.Fatal("invalid save changed configuration")
	}
}

func TestDynamicConfigurationRevisionNormalizationAndHashSensitivity(t *testing.T) {
	db, service := newConfigurationService(t, func(_ context.Context, _ worker.CatalogWorker, _ map[string]string) error { return nil })
	defer db.Close()

	draft := completeDraft(t)
	draft.Container.CapAdd = []string{"SYS_PTRACE", "NET_ADMIN"}
	draft.Workers[0].TaskTypes = []string{"explore", "bootstrap", "reason"}
	draft.Workers = append(draft.Workers, config.DynamicWorkerDraft{
		Name: "codex-second", AgentType: "codex", ProviderName: "Other",
		TaskTypes: []string{"reason", "explore", "bootstrap"}, MaxRunning: 1, Priority: 2,
		AgentEnv: map[string]string{
			"CODEX_MODEL": "model-two", "CODEX_BASE_URL": "https://provider.example.invalid",
		}, Capabilities: []string{"process_recovery"}, TimeoutOverrides: map[string]int{},
	})
	read, err := service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: 1, Draft: draft,
		Secrets: map[string]map[string]config.WorkerSecretChange{
			"codex-deepseek": {"OPENAI_API_KEY": {Value: "secret-one"}},
			"codex-second":   {"OPENAI_API_KEY": {Value: "secret-two"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.ReadActiveRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	equivalent := read.Draft
	slices.Reverse(equivalent.Container.CapAdd)
	slices.Reverse(equivalent.Workers)
	for index := range equivalent.Workers {
		slices.Reverse(equivalent.Workers[index].TaskTypes)
		slices.Reverse(equivalent.Workers[index].Capabilities)
	}
	read, err = service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{ExpectedDraftVersion: 2, Draft: equivalent})
	if err != nil {
		t.Fatal(err)
	}
	reused, err := service.ReadActiveRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if reused.ID != first.ID {
		t.Fatalf("unordered collection order changed Revision ID: first=%q reused=%q", first.ID, reused.ID)
	}

	changed := read.Draft
	changed.Tasks.Reason.PrimarySeconds++
	read, err = service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{ExpectedDraftVersion: 3, Draft: changed})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ReadActiveRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("runtime timeout change reused the previous Revision ID")
	}

	read, err = service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{
		ExpectedDraftVersion: 4, Draft: read.Draft,
		Secrets: map[string]map[string]config.WorkerSecretChange{
			"codex-deepseek": {"OPENAI_API_KEY": {Value: "secret-three"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	third, err := service.ReadActiveRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if third.ID == second.ID {
		t.Fatal("Secret Version change reused the previous Revision ID")
	}
	assertConfigurationRevisionCount(t, db, 3)
}

func TestDynamicConfigurationSavePublicationIsAtomic(t *testing.T) {
	db, service := newConfigurationService(t, func(_ context.Context, _ worker.CatalogWorker, _ map[string]string) error { return nil })
	defer db.Close()

	read := saveCompleteWorker(t, service, "secret-one")
	first, err := service.ReadActiveRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	changed := read.Draft
	changed.Runtime.MaxWorkers++
	if err := execConfigurationSQL(t, db, `
		CREATE TRIGGER reject_active_revision_switch
		BEFORE UPDATE OF active_revision ON configuration_state
		BEGIN SELECT RAISE(ABORT, 'injected pointer failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{ExpectedDraftVersion: 2, Draft: changed}); err == nil {
		t.Fatal("injected active pointer failure was ignored")
	}
	state, err := config.LoadDeploymentState(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveRevision != first.ID {
		t.Fatalf("failed switch changed active Revision to %q", state.ActiveRevision)
	}
	assertConfigurationRevisionCount(t, db, 1)

	if err := execConfigurationSQL(t, db, "DROP TRIGGER reject_active_revision_switch"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveDraft(t.Context(), config.DynamicDraftSaveRequest{ExpectedDraftVersion: 2, Draft: changed}); err != nil {
		t.Fatal(err)
	}
	second, err := service.ReadActiveRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("changed Draft did not switch to a new Revision")
	}
	oldRevision, err := service.ReadRevision(t.Context(), first.ID)
	if err != nil || oldRevision.ID != first.ID {
		t.Fatalf("old Revision was not retained: %+v, %v", oldRevision, err)
	}
	assertConfigurationRevisionCount(t, db, 2)
}

func assertConfigurationRevisionCount(t *testing.T, db *database.DB, want int) {
	t.Helper()
	conn, err := db.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var got int
	if err := conn.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM configuration_revisions").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Config Revision count = %d, want %d", got, want)
	}
}

func readConfigurationRevisionRecord(t *testing.T, db *database.DB, id string) (string, string) {
	t.Helper()
	conn, err := db.GetReadConn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var document, createdBy string
	if err := conn.QueryRowContext(t.Context(), `
		SELECT document_json, created_by FROM configuration_revisions WHERE id = ?`, id,
	).Scan(&document, &createdBy); err != nil {
		t.Fatal(err)
	}
	return document, createdBy
}

func execConfigurationSQL(t *testing.T, db *database.DB, statement string, arguments ...any) error {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(t.Context(), statement, arguments...); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
