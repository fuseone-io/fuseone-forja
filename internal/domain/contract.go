package domain

import "time"

// Contract is the normalised .forja.yaml v1 of a repository. After a
// successful parse every slice and map is non-nil: an omitted section is
// empty, never absent. Limits stay zero when omitted.
type Contract struct {
	Version         int                `json:"version"`
	Image           string             `json:"image"`
	Setup           []string           `json:"setup"`
	Services        map[string]Service `json:"services"`
	Registries      []string           `json:"registries"`
	Gates           []Gate             `json:"gates"`
	Verify          Verify             `json:"verify"`
	ProofPaths      []string           `json:"proof_paths"`
	HumanMergePaths []string           `json:"human_merge_paths"`
	Limits          Limits             `json:"limits"`
	AgentContext    []string           `json:"agent_context"`
}

// Service is a sidecar the gates may depend on.
type Service struct {
	Image string            `json:"image"`
	Env   map[string]string `json:"env"`
}

// Gate is one of the repository's own commands; its exit code is the verdict.
type Gate struct {
	Name string `json:"name"`
	Run  string `json:"run"`
	// Needs names the services that must be up before the gate runs.
	Needs []string `json:"needs"`
}

// Verify configures what the QA inversions leave alone.
type Verify struct {
	Exclude []string `json:"exclude"`
}

// Limits bounds a run.
type Limits struct {
	WallClock    time.Duration `json:"wall_clock"`
	MaxDiffLines int           `json:"max_diff_lines"`
}

// ContractSource is the raw contract as read from the base commit: the
// commit SHA it was read at and the file's bytes.
type ContractSource struct {
	SHA  string
	Body []byte
}
