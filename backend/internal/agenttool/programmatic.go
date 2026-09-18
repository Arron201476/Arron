package agenttool

import (
	"fmt"
	"slices"
	"strings"
)

const ProgrammaticOptionID = "programmatic:runtime"

// Programmatic execution grants are operator-owned, never supplied by a Skill.
type ProgrammaticConfig struct {
	Enabled      bool     `json:"enabled"`
	WorkspaceIDs []string `json:"workspace_ids,omitempty"`
	ToolIDs      []string `json:"tool_ids"`
}

func cloneProgrammaticConfig(config ProgrammaticConfig) ProgrammaticConfig {
	config.WorkspaceIDs = slices.Clone(config.WorkspaceIDs)
	config.ToolIDs = slices.Clone(config.ToolIDs)
	return config
}

func (r *Registry) validateProgrammaticConfig() error {
	config := &r.config.Programmatic
	if config.Enabled && len(config.ToolIDs) == 0 {
		return fmt.Errorf("programmatic_tool_calling requires explicit tool_ids")
	}
	seen := make(map[string]bool)
	for _, id := range config.ToolIDs {
		descriptor, exists := r.Get(id)
		if seen[id] || !exists || descriptor.Kind != KindRuntimeFunction || descriptor.Access != AccessRead || descriptor.Approval != ApprovalNever {
			return fmt.Errorf("programmatic_tool_calling tool %q must be a unique read-only runtime tool without approval", id)
		}
		seen[id] = true
	}
	seen = make(map[string]bool)
	for _, id := range config.WorkspaceIDs {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id || seen[id] {
			return fmt.Errorf("programmatic_tool_calling workspace_ids must contain unique nonempty IDs")
		}
		seen[id] = true
	}
	slices.Sort(config.ToolIDs)
	slices.Sort(config.WorkspaceIDs)
	return nil
}

func (r *Registry) programmaticToolIDs() []string {
	if !r.config.Programmatic.Enabled {
		return nil
	}
	return slices.Clone(r.config.Programmatic.ToolIDs)
}
