// Package tfplan reads the JSON form of a Terraform plan, as produced by
// `terraform show -json tfplan`. Only the parts needed to reason about
// destroys and creates are modelled; everything else in the document is
// ignored on purpose, so a newer Terraform format version still parses.
package tfplan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Plan is the subset of the plan JSON this tool cares about.
type Plan struct {
	FormatVersion    string           `json:"format_version"`
	TerraformVersion string           `json:"terraform_version"`
	ResourceChanges  []ResourceChange `json:"resource_changes"`
}

// ResourceChange is one entry of the plan's resource_changes array.
type ResourceChange struct {
	Address       string `json:"address"`
	ModuleAddress string `json:"module_address"`
	Mode          string `json:"mode"`
	Type          string `json:"type"`
	Name          string `json:"name"`
	Index         any    `json:"index"`
	ProviderName  string `json:"provider_name"`
	Change        Change `json:"change"`
}

// Change carries the planned actions and the attribute snapshots around them.
type Change struct {
	Actions      []string       `json:"actions"`
	Before       map[string]any `json:"before"`
	After        map[string]any `json:"after"`
	AfterUnknown map[string]any `json:"after_unknown"`
}

// Kind is the coarse classification of a change that matters for pairing.
type Kind string

const (
	// KindCreate is a plain create: a candidate destination for a moved block.
	KindCreate Kind = "create"
	// KindDelete is a plain delete: a candidate source for a moved block.
	KindDelete Kind = "delete"
	// KindReplace is a destroy+create of the same address. The address does not
	// change, so no moved block can help; it is reported separately.
	KindReplace Kind = "replace"
	// KindOther is no-op, update, read and anything a future Terraform adds.
	KindOther Kind = "other"
)

// Kind classifies the planned actions. Terraform writes ["delete","create"]
// for a replace and ["create","delete"] when create_before_destroy is set;
// both land in KindReplace.
func (c Change) Kind() Kind {
	var create, remove, other bool
	for _, a := range c.Actions {
		switch a {
		case "create":
			create = true
		case "delete":
			remove = true
		default:
			other = true
		}
	}
	switch {
	case other:
		return KindOther
	case create && remove:
		return KindReplace
	case create:
		return KindCreate
	case remove:
		return KindDelete
	}
	return KindOther
}

// Load parses a plan JSON document.
func Load(r io.Reader) (*Plan, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse plan JSON: %w", err)
	}
	if p.ResourceChanges == nil {
		return nil, errors.New("plan JSON has no resource_changes array; pass the output of `terraform show -json tfplan`")
	}
	return &p, nil
}
