// Package gates is the pure policy layer for the gate engine. It parses a run's
// gates_json into a Policy and answers "what mode governs gate kind K?" with no
// I/O. The orchestrator composes it with the store to create and resolve gates.
package gates

import (
	"encoding/json"
	"fmt"
)

// Mode is how an automatic gate kind is resolved.
type Mode string

const (
	ModeAuto            Mode = "auto"
	ModeRequireApproval Mode = "require_approval"
)

// Policy maps gate kinds (plan, merge, worker_done) to their mode.
type Policy struct {
	modes map[string]Mode
}

type gateConfig struct {
	Mode Mode `json:"mode"`
}

// Parse reads a gates_json document into a Policy. The "custom" array (and any
// non-object field) is ignored. An empty string is treated as an empty policy
// rather than an error. Mode values other than auto/require_approval are
// rejected.
func Parse(gatesJSON string) (Policy, error) {
	p := Policy{modes: map[string]Mode{}}
	if gatesJSON == "" {
		return p, nil
	}
	// Decode permissively: known automatic-gate keys are objects with a "mode";
	// other keys (e.g. "custom": []) are tolerated and skipped.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(gatesJSON), &raw); err != nil {
		return Policy{}, fmt.Errorf("parse gates_json: %w", err)
	}
	for kind, msg := range raw {
		var cfg gateConfig
		if err := json.Unmarshal(msg, &cfg); err != nil {
			// Not a {mode: ...} object (e.g. custom: []) — skip.
			continue
		}
		if cfg.Mode == "" {
			continue
		}
		if cfg.Mode != ModeAuto && cfg.Mode != ModeRequireApproval {
			return Policy{}, fmt.Errorf("gate %q: unknown mode %q", kind, cfg.Mode)
		}
		p.modes[kind] = cfg.Mode
	}
	return p, nil
}

// Mode returns the mode governing gate kind, defaulting to require_approval for
// any kind not explicitly configured (never auto-approve the unspecified).
func (p Policy) Mode(kind string) Mode {
	if m, ok := p.modes[kind]; ok {
		return m
	}
	return ModeRequireApproval
}
