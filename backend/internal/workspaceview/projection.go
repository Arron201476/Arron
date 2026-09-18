package workspaceview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"content-agent/backend/internal/capability"
)

const ContractVersion = "1.0.0"

type Catalog struct {
	ContractVersion string     `json:"contract_version"`
	Revision        string     `json:"revision"`
	Registries      Registries `json:"registries"`
}

type Registries struct {
	Artifacts    []ArtifactEntry    `json:"artifacts"`
	Navigation   []NavigationEntry  `json:"navigation"`
	Tasks        []TaskEntry        `json:"tasks"`
	Approvals    []ApprovalEntry    `json:"approvals"`
	Interactions []InteractionEntry `json:"interactions"`
	Composer     []ComposerEntry    `json:"composer"`
}

type ArtifactEntry struct {
	ArtifactType         string   `json:"artifact_type"`
	ViewKey              string   `json:"view_key"`
	Label                string   `json:"label"`
	Description          string   `json:"description"`
	Editable             bool     `json:"editable"`
	PreferredFields      []string `json:"preferred_fields"`
	AvailableActions     []string `json:"available_actions"`
	CollectionMemberType string   `json:"collection_member_type,omitempty"`
}

type NavigationEntry struct {
	ArtifactType string `json:"artifact_type"`
	Order        int    `json:"order"`
	GroupMode    string `json:"group_mode"`
	Visibility   string `json:"visibility"`
}

type TaskEntry struct {
	Status  string `json:"status"`
	ViewKey string `json:"view_key"`
}

type ApprovalMatch struct {
	Scope       string `json:"scope,omitempty"`
	Option      string `json:"option,omitempty"`
	SubjectKind string `json:"subject_kind,omitempty"`
}

type ApprovalEntry struct {
	RegistryKey string        `json:"registry_key"`
	ViewKey     string        `json:"view_key"`
	Match       ApprovalMatch `json:"match"`
}

type InteractionEntry struct {
	RegistryKey string   `json:"registry_key"`
	ViewKey     string   `json:"view_key"`
	Commands    []string `json:"commands"`
}

type ComposerConfig struct {
	ViewKey    string   `json:"view_key"`
	DefaultRef string   `json:"default_ref,omitempty"`
	Options    []string `json:"options"`
}

type ComposerEntry struct {
	CapabilityID       string                  `json:"capability_id"`
	Version            string                  `json:"version"`
	Label              string                  `json:"label"`
	Description        string                  `json:"description"`
	Status             capability.Availability `json:"status"`
	ReasonCode         string                  `json:"reason_code,omitempty"`
	UserMessage        string                  `json:"user_message,omitempty"`
	ExecutionMode      string                  `json:"execution_mode"`
	CreatesRun         bool                    `json:"creates_run"`
	ViewKey            string                  `json:"view_key"`
	IconKey            string                  `json:"icon_key"`
	MenuOrder          int                     `json:"menu_order"`
	DefaultPrompt      string                  `json:"default_prompt,omitempty"`
	AcceptedAssetKinds []string                `json:"accepted_asset_kinds"`
	InputBinding       capability.InputBinding `json:"input_binding,omitempty"`
	EntryPolicy        capability.EntryPolicy  `json:"entry_policy"`
	Config             ComposerConfig          `json:"config"`
	Skill              *capability.PublicSkill `json:"skill,omitempty"`
}

func Compile(
	capabilities []capability.PublicCapability,
	presentations []capability.ArtifactPresentation,
) Catalog {
	registries := Registries{
		Artifacts:    compileArtifacts(presentations),
		Navigation:   compileNavigation(presentations),
		Tasks:        defaultTasks(),
		Approvals:    defaultApprovals(),
		Interactions: defaultInteractions(),
		Composer:     compileComposer(capabilities),
	}
	payload, _ := json.Marshal(registries)
	digest := sha256.Sum256(payload)
	return Catalog{
		ContractVersion: ContractVersion,
		Revision:        "sha256:" + hex.EncodeToString(digest[:]),
		Registries:      registries,
	}
}

func compileArtifacts(presentations []capability.ArtifactPresentation) []ArtifactEntry {
	items := make([]ArtifactEntry, 0, len(presentations))
	for _, presentation := range presentations {
		items = append(items, ArtifactEntry{
			ArtifactType:         presentation.ArtifactType,
			ViewKey:              presentation.Renderer,
			Label:                presentation.Label,
			Description:          presentation.Description,
			Editable:             presentation.Editable,
			PreferredFields:      cloneStrings(presentation.PreferredFields),
			AvailableActions:     cloneStrings(presentation.AvailableActions),
			CollectionMemberType: presentation.CollectionMemberType,
		})
	}
	sort.SliceStable(items, func(left, right int) bool {
		return items[left].ArtifactType < items[right].ArtifactType
	})
	return items
}

func compileNavigation(presentations []capability.ArtifactPresentation) []NavigationEntry {
	items := make([]NavigationEntry, 0, len(presentations))
	for _, presentation := range presentations {
		items = append(items, NavigationEntry{
			ArtifactType: presentation.ArtifactType,
			Order:        presentation.Navigation.Order,
			GroupMode:    presentation.Navigation.GroupMode,
			Visibility:   presentation.Navigation.Visibility,
		})
	}
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].Order != items[right].Order {
			return items[left].Order < items[right].Order
		}
		return items[left].ArtifactType < items[right].ArtifactType
	})
	return items
}

func compileComposer(capabilities []capability.PublicCapability) []ComposerEntry {
	items := make([]ComposerEntry, 0, len(capabilities))
	for _, item := range capabilities {
		viewKey := strings.TrimSpace(item.UIEntry.EntryViewKey)
		if viewKey == "" {
			viewKey = "inspector"
		}
		configViewKey := strings.TrimSpace(item.UIEntry.ConfigViewKey)
		if configViewKey == "" {
			configViewKey = "none"
		}
		items = append(items, ComposerEntry{
			CapabilityID:       item.CapabilityID,
			Version:            item.Version,
			Label:              item.Label,
			Description:        item.Description,
			Status:             item.Status,
			ReasonCode:         item.ReasonCode,
			UserMessage:        item.UserMessage,
			ExecutionMode:      item.ExecutionMode,
			CreatesRun:         item.CreatesRun,
			ViewKey:            viewKey,
			IconKey:            item.UIEntry.IconKey,
			MenuOrder:          item.UIEntry.MenuOrder,
			DefaultPrompt:      item.UIEntry.DefaultPrompt,
			AcceptedAssetKinds: cloneStrings(item.AcceptedAssetKinds),
			InputBinding:       item.InputBinding,
			EntryPolicy:        item.EntryPolicy,
			Config: ComposerConfig{
				ViewKey:    configViewKey,
				DefaultRef: item.DefaultConfigRef,
				Options:    cloneStrings(item.ConfigOptions),
			},
			Skill: clonePublicSkill(item.Skill),
		})
	}
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].MenuOrder != items[right].MenuOrder {
			return items[left].MenuOrder < items[right].MenuOrder
		}
		return items[left].CapabilityID < items[right].CapabilityID
	})
	return items
}

func defaultTasks() []TaskEntry {
	return []TaskEntry{
		{Status: "queued", ViewKey: "task_progress"},
		{Status: "running", ViewKey: "task_progress"},
		{Status: "waiting_approval", ViewKey: "task_progress"},
		{Status: "pausing", ViewKey: "task_progress"},
		{Status: "paused", ViewKey: "task_progress"},
		{Status: "completed", ViewKey: "task_result"},
		{Status: "failed", ViewKey: "task_failure"},
		{Status: "cancelled", ViewKey: "task_result"},
	}
}

func defaultApprovals() []ApprovalEntry {
	return []ApprovalEntry{
		{RegistryKey: "quality_review", ViewKey: "quality_review", Match: ApprovalMatch{Scope: "quality_review"}},
		{RegistryKey: "adaptation_strategy", ViewKey: "adaptation_strategy", Match: ApprovalMatch{Option: "select_adaptation_strategy"}},
		{RegistryKey: "single_option", ViewKey: "single_option", Match: ApprovalMatch{Option: "select_single_option"}},
		{RegistryKey: "workflow_transition", ViewKey: "action_list", Match: ApprovalMatch{Scope: "workflow_transition", SubjectKind: "transition"}},
		{RegistryKey: "volume_fit", ViewKey: "volume_fit", Match: ApprovalMatch{SubjectKind: "transition"}},
		{RegistryKey: "action_list", ViewKey: "action_list", Match: ApprovalMatch{}},
	}
}

func defaultInteractions() []InteractionEntry {
	return []InteractionEntry{
		{RegistryKey: "run", ViewKey: "run_controls", Commands: []string{"pause", "resume", "cancel", "retry_failed"}},
		{RegistryKey: "artifact", ViewKey: "artifact_actions", Commands: []string{"inspect", "edit_artifact", "request_ai_revision", "regenerate_artifact"}},
		{RegistryKey: "agent_tool", ViewKey: "agent_tool_approval", Commands: []string{"approve", "deny"}},
		{RegistryKey: "revision", ViewKey: "revision_actions", Commands: []string{"preview", "accept", "reject", "cancel"}},
	}
}

func cloneStrings(source []string) []string {
	if source == nil {
		return []string{}
	}
	return append([]string(nil), source...)
}

func clonePublicSkill(source *capability.PublicSkill) *capability.PublicSkill {
	if source == nil {
		return nil
	}
	result := *source
	result.Dependencies = append([]capability.SkillDependency(nil), source.Dependencies...)
	return &result
}
