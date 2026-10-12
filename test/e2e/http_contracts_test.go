package e2e

import "encoding/json"

type projectMetaWire struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Generation int    `json:"generation"`
	Revision   int64  `json:"revision"`
}

type factWire struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type projectDetailWire struct {
	Project projectMetaWire   `json:"project"`
	Facts   []factWire        `json:"facts"`
	Intents []json.RawMessage `json:"intents"`
	Hints   []json.RawMessage `json:"hints"`
}

type hintReceiptWire struct {
	Hint struct {
		ID      string `json:"id"`
		Creator string `json:"creator"`
	} `json:"hint"`
	ProjectRevision int64 `json:"project_revision"`
}

type impactWire struct {
	ProjectRevision int64 `json:"project_revision"`
}

type lifecycleReceiptWire struct {
	Project         projectMetaWire `json:"project"`
	ProjectRevision int64           `json:"project_revision"`
	Revision        int64           `json:"revision"`
}

type completeReceiptWire struct {
	Project    projectMetaWire `json:"project"`
	Completion struct {
		Actor string `json:"actor"`
	} `json:"completion"`
}

type reopenReceiptWire struct {
	Project projectMetaWire `json:"project"`
}

type renameReceiptWire struct {
	Project         projectMetaWire `json:"project"`
	ProjectRevision int64           `json:"project_revision"`
}

type deleteReceiptWire struct {
	ProjectID    string `json:"project_id"`
	CleanupJobID string `json:"cleanup_job_id"`
}

type activityWire struct {
	Runs []json.RawMessage `json:"runs"`
}

type correctionReceiptWire struct {
	Original struct {
		Status string `json:"status"`
	} `json:"original"`
	Replacement struct {
		Description string `json:"description"`
	} `json:"replacement"`
	ProjectRevision int64 `json:"project_revision"`
}

type changeReplayWire struct {
	CurrentRevision int64 `json:"current_revision"`
	Changes         []struct {
		Kind string `json:"kind"`
	} `json:"changes"`
}
