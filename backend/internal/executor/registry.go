package executor

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"content-agent/backend/internal/capability"
)

type Kind string

const (
	KindRuntime  Kind = "runtime"
	KindWorker   Kind = "worker"
	KindWorkflow Kind = "workflow"
)

type Descriptor struct {
	ID        string
	Kind      Kind
	Claimable bool
}

type Registry struct {
	entries map[string]Descriptor
}

type ProviderSet map[string]bool

type CapabilityAssessment struct {
	CapabilityID string
	Available    bool
	ReasonCode   string
	Message      string
	Missing      []string
}

type capabilityAvailabilityResolver struct {
	registry  *Registry
	providers ProviderSet
}

func (r capabilityAvailabilityResolver) ResolveCapabilityAvailability(
	entry capability.Entry,
) capability.CapabilityAvailabilityResolution {
	assessment := r.registry.Assess(entry, r.providers)
	return capability.CapabilityAvailabilityResolution{
		Available:  assessment.Available,
		ReasonCode: assessment.ReasonCode,
		Message:    assessment.Message,
	}
}

func New(descriptors []Descriptor) (*Registry, error) {
	registry := &Registry{entries: make(map[string]Descriptor, len(descriptors))}
	for _, descriptor := range descriptors {
		descriptor.ID = strings.TrimSpace(descriptor.ID)
		if descriptor.ID == "" || descriptor.Kind == "" {
			return nil, fmt.Errorf("executor descriptor is incomplete")
		}
		if _, exists := registry.entries[descriptor.ID]; exists {
			return nil, fmt.Errorf("executor %q is duplicated", descriptor.ID)
		}
		registry.entries[descriptor.ID] = descriptor
	}
	return registry, nil
}

func NewDefault() *Registry {
	registry, err := New([]Descriptor{
		{ID: "runtime.ingest_source", Kind: KindRuntime},
		{ID: "runtime.build_source_manifest", Kind: KindRuntime},
		{ID: "runtime.review_volume_fit", Kind: KindRuntime},
		{ID: "runtime.build_script_contexts", Kind: KindRuntime},
		{ID: "runtime.aggregate_scripts", Kind: KindRuntime},
		{ID: "runtime.aggregate_reference_scripts", Kind: KindRuntime},
		{ID: "worker.structured_content", Kind: KindWorker, Claimable: true},
		{ID: "workflow.novel_episode_split", Kind: KindWorkflow, Claimable: true},
		{ID: "workflow.episode_cards", Kind: KindWorkflow, Claimable: true},
		{ID: "workflow.shared_script_generation", Kind: KindWorkflow, Claimable: true},
		{ID: "workflow.shared_script_quality_review", Kind: KindWorkflow, Claimable: true},
		{ID: "workflow.non_novel_story_seed", Kind: KindWorkflow, Claimable: true},
		{ID: "workflow.non_novel_series_blueprint", Kind: KindWorkflow, Claimable: true},
		{ID: "workflow.video_script_extract", Kind: KindWorkflow, Claimable: true},
	})
	if err != nil {
		panic(err)
	}
	return registry
}

func (r *Registry) Get(id string) (Descriptor, bool) {
	if r == nil {
		return Descriptor{}, false
	}
	descriptor, ok := r.entries[id]
	return descriptor, ok
}

func (r *Registry) WorkerExecutorIDs() []string {
	if r == nil {
		return nil
	}
	result := make([]string, 0, len(r.entries))
	for _, descriptor := range r.entries {
		if descriptor.Claimable {
			result = append(result, descriptor.ID)
		}
	}
	sort.Strings(result)
	return result
}

func (r *Registry) Assess(
	entry capability.Entry,
	providers ProviderSet,
) CapabilityAssessment {
	assessment := CapabilityAssessment{CapabilityID: entry.CapabilityID}
	if entry.Status != capability.Available || entry.Definition == nil {
		assessment.ReasonCode = entry.ReasonCode
		assessment.Message = entry.Message
		return assessment
	}
	missingExecutors := make([]string, 0)
	for _, step := range entry.Definition.Steps {
		if _, ok := r.Get(step.ExecutorRef); !ok && !slices.Contains(missingExecutors, step.ExecutorRef) {
			missingExecutors = append(missingExecutors, step.ExecutorRef)
		}
	}
	if len(missingExecutors) > 0 {
		sort.Strings(missingExecutors)
		assessment.ReasonCode = "EXECUTOR_UNAVAILABLE"
		assessment.Message = "该能力所需执行器尚未接通。"
		assessment.Missing = missingExecutors
		return assessment
	}
	missingProviders := make([]string, 0)
	for _, provider := range entry.Definition.RequiredProviders {
		if !providers[provider] {
			missingProviders = append(missingProviders, provider)
		}
	}
	if len(missingProviders) > 0 {
		sort.Strings(missingProviders)
		assessment.ReasonCode = "PROVIDER_UNAVAILABLE"
		assessment.Message = "该能力所需服务尚未配置。"
		assessment.Missing = missingProviders
		return assessment
	}
	assessment.Available = true
	return assessment
}

func (r *Registry) ApplyAvailability(
	capabilities *capability.Registry,
	providers ProviderSet,
) []CapabilityAssessment {
	if capabilities == nil {
		return nil
	}
	entries := capabilities.Entries()
	result := make([]CapabilityAssessment, 0, len(entries))
	for _, entry := range entries {
		assessment := r.Assess(entry, providers)
		result = append(result, assessment)
	}
	_ = capabilities.SetCapabilityAvailabilityResolver(capabilityAvailabilityResolver{
		registry: r, providers: providers,
	})
	return result
}
