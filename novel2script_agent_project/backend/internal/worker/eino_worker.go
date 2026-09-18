package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/compose"

	"novel2script-agent/backend/internal/agent"
	agentruntime "novel2script-agent/backend/internal/agent/runtime"
)

// EinoWorker compiles the content execution graphs once at startup. Each graph
// has explicit input validation, model execution, and output validation nodes;
// persistence and HTTP remain runtime concerns.
type EinoWorker struct {
	planGraph   compose.Runnable[planStepInput, planStepOutput]
	scriptGraph compose.Runnable[scriptStepInput, scriptStepOutput]
	patchGraph  compose.Runnable[patchStepInput, patchStepOutput]
	splitGraph  compose.Runnable[splitStageInput, splitStageOutput]
	cardsGraph  compose.Runnable[cardsBatchInput, cardsBatchOutput]
}

type planStepInput struct {
	Run          agent.Run
	Source       agent.Artifact
	Existing     []agent.Artifact
	ArtifactType string
}

type planStepOutput struct {
	Artifact              agentruntime.PlannedArtifact
	Approval              *agent.ApprovalRequest
	RequestedArtifactType string
}

type scriptStepInput struct {
	Run          agent.Run
	Artifacts    []agent.Artifact
	ArtifactType string
	Note         string
}

type scriptStepOutput struct {
	Artifact              agentruntime.PlannedArtifact
	RequestedArtifactType string
}

type patchStepInput struct {
	Run          agent.Run
	Target       agent.Artifact
	Existing     []agent.Artifact
	ArtifactType string
	Instruction  string
}

type patchStepOutput struct {
	Artifact              agentruntime.PlannedArtifact
	RequestedArtifactType string
}

type splitStageInput struct {
	Run     agent.Run
	Request agentruntime.EpisodeSplitStageRequest
}

type splitStageOutput struct {
	Result agentruntime.EpisodeSplitStageResult
	Stage  agentruntime.EpisodeSplitStage
}

type cardsBatchInput struct {
	Run     agent.Run
	Request agentruntime.EpisodeCardsBatchRequest
}

type cardsBatchOutput struct {
	Result agentruntime.EpisodeCardsBatchResult
	Start  int
	End    int
}

func NewEinoWorker(delegate *LLMWorker) (*EinoWorker, error) {
	if delegate == nil {
		return nil, fmt.Errorf("eino worker delegate is required")
	}

	planGraph, err := compileEinoExecutionGraph(
		"novel2script_plan_artifact",
		validatePlanInput,
		func(ctx context.Context, in planStepInput) (planStepOutput, error) {
			artifact, approval, err := delegate.PlanStep(ctx, in.Run, in.Source, in.Existing, in.ArtifactType)
			return planStepOutput{Artifact: artifact, Approval: approval, RequestedArtifactType: in.ArtifactType}, err
		},
		func(_ context.Context, out planStepOutput) (planStepOutput, error) {
			return out, validatePlannedArtifact(out.RequestedArtifactType, out.Artifact)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("compile planning graph: %w", err)
	}

	scriptGraph, err := compileEinoExecutionGraph(
		"novel2script_write_script",
		validateScriptInput,
		func(ctx context.Context, in scriptStepInput) (scriptStepOutput, error) {
			artifact, err := delegate.WriteScriptStep(ctx, in.Run, in.Artifacts, in.ArtifactType, in.Note)
			return scriptStepOutput{Artifact: artifact, RequestedArtifactType: in.ArtifactType}, err
		},
		func(_ context.Context, out scriptStepOutput) (scriptStepOutput, error) {
			return out, validatePlannedArtifact(out.RequestedArtifactType, out.Artifact)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("compile script graph: %w", err)
	}

	patchGraph, err := compileEinoExecutionGraph(
		"novel2script_patch_artifact",
		validatePatchInput,
		func(ctx context.Context, in patchStepInput) (patchStepOutput, error) {
			artifact, err := delegate.PatchArtifactStep(ctx, in.Run, in.Target, in.Existing, in.ArtifactType, in.Instruction)
			return patchStepOutput{Artifact: artifact, RequestedArtifactType: in.ArtifactType}, err
		},
		func(_ context.Context, out patchStepOutput) (patchStepOutput, error) {
			return out, validatePlannedArtifact(out.RequestedArtifactType, out.Artifact)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("compile patch graph: %w", err)
	}

	splitGraph, err := compileEinoExecutionGraph(
		"novel2script_episode_split_stage",
		validateSplitStageInput,
		func(ctx context.Context, in splitStageInput) (splitStageOutput, error) {
			result, err := delegate.PlanEpisodeSplitStage(ctx, in.Run, in.Request)
			return splitStageOutput{Result: result, Stage: in.Request.Stage}, err
		},
		validateSplitStageOutput,
	)
	if err != nil {
		return nil, fmt.Errorf("compile episode split graph: %w", err)
	}

	cardsGraph, err := compileEinoExecutionGraph(
		"novel2script_episode_cards_batch",
		validateCardsBatchInput,
		func(ctx context.Context, in cardsBatchInput) (cardsBatchOutput, error) {
			result, err := delegate.PlanEpisodeCardsBatch(ctx, in.Run, in.Request)
			return cardsBatchOutput{Result: result, Start: in.Request.BatchStart, End: in.Request.BatchEnd}, err
		},
		validateCardsBatchOutput,
	)
	if err != nil {
		return nil, fmt.Errorf("compile episode cards graph: %w", err)
	}

	return &EinoWorker{planGraph: planGraph, scriptGraph: scriptGraph, patchGraph: patchGraph, splitGraph: splitGraph, cardsGraph: cardsGraph}, nil
}

func (w *EinoWorker) PlanStep(ctx context.Context, run agent.Run, source agent.Artifact, existing []agent.Artifact, artifactType string) (agentruntime.PlannedArtifact, *agent.ApprovalRequest, error) {
	out, err := w.planGraph.Invoke(ctx, planStepInput{Run: run, Source: source, Existing: existing, ArtifactType: artifactType})
	return out.Artifact, out.Approval, err
}

func (w *EinoWorker) WriteScriptStep(ctx context.Context, run agent.Run, artifacts []agent.Artifact, artifactType string, note string) (agentruntime.PlannedArtifact, error) {
	out, err := w.scriptGraph.Invoke(ctx, scriptStepInput{Run: run, Artifacts: artifacts, ArtifactType: artifactType, Note: note})
	return out.Artifact, err
}

func (w *EinoWorker) PatchArtifactStep(ctx context.Context, run agent.Run, target agent.Artifact, existing []agent.Artifact, artifactType string, instruction string) (agentruntime.PlannedArtifact, error) {
	out, err := w.patchGraph.Invoke(ctx, patchStepInput{Run: run, Target: target, Existing: existing, ArtifactType: artifactType, Instruction: instruction})
	return out.Artifact, err
}

func (w *EinoWorker) PlanEpisodeSplitStage(ctx context.Context, run agent.Run, request agentruntime.EpisodeSplitStageRequest) (agentruntime.EpisodeSplitStageResult, error) {
	out, err := w.splitGraph.Invoke(ctx, splitStageInput{Run: run, Request: request})
	return out.Result, err
}

func (w *EinoWorker) PlanEpisodeCardsBatch(ctx context.Context, run agent.Run, request agentruntime.EpisodeCardsBatchRequest) (agentruntime.EpisodeCardsBatchResult, error) {
	out, err := w.cardsGraph.Invoke(ctx, cardsBatchInput{Run: run, Request: request})
	return out.Result, err
}

func compileEinoExecutionGraph[I, O any](name string, validateInput func(context.Context, I) (I, error), invoke func(context.Context, I) (O, error), validateOutput func(context.Context, O) (O, error)) (compose.Runnable[I, O], error) {
	graph := compose.NewGraph[I, O]()
	if err := graph.AddLambdaNode("validate_input", compose.InvokableLambda(validateInput)); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode("invoke_model", compose.InvokableLambda(invoke)); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode("validate_output", compose.InvokableLambda(validateOutput)); err != nil {
		return nil, err
	}
	if err := graph.AddEdge(compose.START, "validate_input"); err != nil {
		return nil, err
	}
	if err := graph.AddEdge("validate_input", "invoke_model"); err != nil {
		return nil, err
	}
	if err := graph.AddEdge("invoke_model", "validate_output"); err != nil {
		return nil, err
	}
	if err := graph.AddEdge("validate_output", compose.END); err != nil {
		return nil, err
	}
	return graph.Compile(context.Background(), compose.WithGraphName(name))
}

func validatePlanInput(_ context.Context, in planStepInput) (planStepInput, error) {
	if err := validateExecutionInput(in.Run, in.ArtifactType); err != nil {
		return in, err
	}
	if in.Source.Payload == nil {
		return in, fmt.Errorf("planning source payload is required")
	}
	return in, nil
}

func validateScriptInput(_ context.Context, in scriptStepInput) (scriptStepInput, error) {
	if err := validateExecutionInput(in.Run, in.ArtifactType); err != nil {
		return in, err
	}
	return in, nil
}

func validatePatchInput(_ context.Context, in patchStepInput) (patchStepInput, error) {
	if err := validateExecutionInput(in.Run, in.ArtifactType); err != nil {
		return in, err
	}
	if in.Target.Payload == nil {
		return in, fmt.Errorf("patch target payload is required")
	}
	if strings.TrimSpace(in.Instruction) == "" {
		return in, fmt.Errorf("patch instruction is required")
	}
	return in, nil
}

func validateSplitStageInput(_ context.Context, in splitStageInput) (splitStageInput, error) {
	if in.Run.SourceMode != agent.SourceModeNovel {
		return in, fmt.Errorf("episode split stages require novel source mode")
	}
	if in.Request.TargetEpisodeCount < 1 || len(in.Request.SourceUnits) == 0 {
		return in, fmt.Errorf("episode split stage requires target episodes and source units")
	}
	if in.Request.Stage != agentruntime.EpisodeSplitStageGlobal && in.Request.Stage != agentruntime.EpisodeSplitStageBatch {
		return in, fmt.Errorf("unsupported episode split stage %q", in.Request.Stage)
	}
	if in.Request.Stage == agentruntime.EpisodeSplitStageBatch {
		if in.Request.BatchStart < 1 || in.Request.BatchEnd < in.Request.BatchStart || len(in.Request.Candidates) == 0 {
			return in, fmt.Errorf("episode split batch requires a valid range and candidates")
		}
	}
	return in, nil
}

func validateSplitStageOutput(_ context.Context, out splitStageOutput) (splitStageOutput, error) {
	if out.Stage == agentruntime.EpisodeSplitStageGlobal && len(out.Result.GlobalPlan) == 0 {
		return out, fmt.Errorf("episode split global stage returned no plan")
	}
	if out.Stage == agentruntime.EpisodeSplitStageBatch && len(out.Result.Episodes) == 0 {
		return out, fmt.Errorf("episode split batch stage returned no episodes")
	}
	return out, nil
}

func validateCardsBatchInput(_ context.Context, in cardsBatchInput) (cardsBatchInput, error) {
	if in.Run.SourceMode != agent.SourceModeNovel && in.Run.SourceMode != agent.SourceModeNonNovel {
		return in, fmt.Errorf("episode cards batch requires a resolved source mode")
	}
	if in.Request.TargetEpisodeCount < 1 || in.Request.BatchStart < 1 || in.Request.BatchEnd < in.Request.BatchStart || in.Request.BatchEnd > in.Request.TargetEpisodeCount {
		return in, fmt.Errorf("episode cards batch requires a valid range")
	}
	if len(in.Request.Upstream) == 0 {
		return in, fmt.Errorf("episode cards batch requires upstream artifacts")
	}
	return in, nil
}

func validateCardsBatchOutput(_ context.Context, out cardsBatchOutput) (cardsBatchOutput, error) {
	// Exact batch coverage is validated by Runtime so empty and partial model
	// output share the same recoverable, user-facing failure contract.
	return out, nil
}

func validateExecutionInput(run agent.Run, artifactType string) error {
	if run.SourceMode != agent.SourceModeNovel && run.SourceMode != agent.SourceModeNonNovel {
		return fmt.Errorf("unsupported source mode: %s", run.SourceMode)
	}
	if strings.TrimSpace(artifactType) == "" {
		return fmt.Errorf("artifact type is required")
	}
	return nil
}

func validatePlannedArtifact(requestedType string, artifact agentruntime.PlannedArtifact) error {
	if strings.TrimSpace(artifact.ArtifactType) == "" {
		return fmt.Errorf("model output artifact_type is required")
	}
	if artifact.ArtifactType != requestedType {
		return fmt.Errorf("model output artifact_type %q does not match requested %q", artifact.ArtifactType, requestedType)
	}
	if artifact.Payload == nil {
		return fmt.Errorf("model output payload is required for %s", requestedType)
	}
	return nil
}
