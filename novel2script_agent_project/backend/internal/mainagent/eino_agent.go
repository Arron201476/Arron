package mainagent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/compose"
)

type einoDecisionState struct {
	Input       Context
	Model       Decision
	ApplyGuards bool
}

// EinoAgent separates model interpretation and deterministic guards into a
// compiled Eino graph. Business state and action execution stay in the Go
// runtime so Eino never becomes a second project or run store.
type EinoAgent struct {
	runnable compose.Runnable[Context, Decision]
}

func NewEinoAgent(delegate *Agent) (*EinoAgent, error) {
	if delegate == nil {
		return nil, fmt.Errorf("main agent delegate is required")
	}

	graph := compose.NewGraph[Context, Decision]()
	if err := graph.AddLambdaNode("interpret_intent", compose.InvokableLambda(func(ctx context.Context, input Context) (einoDecisionState, error) {
		decision, applyGuards := delegate.decideModel(ctx, input)
		return einoDecisionState{Input: input, Model: decision, ApplyGuards: applyGuards}, nil
	})); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode("apply_business_guards", compose.InvokableLambda(func(_ context.Context, state einoDecisionState) (Decision, error) {
		guarded := state.Model
		if state.ApplyGuards {
			guarded = normalizeDecision(state.Model, state.Input)
		}
		guarded.Orchestrator = "eino"
		return withDecisionTrace(state.Model, guarded), nil
	})); err != nil {
		return nil, err
	}
	if err := graph.AddEdge(compose.START, "interpret_intent"); err != nil {
		return nil, err
	}
	if err := graph.AddEdge("interpret_intent", "apply_business_guards"); err != nil {
		return nil, err
	}
	if err := graph.AddEdge("apply_business_guards", compose.END); err != nil {
		return nil, err
	}
	runnable, err := graph.Compile(context.Background(), compose.WithGraphName("novel2script_main_agent"))
	if err != nil {
		return nil, err
	}
	return &EinoAgent{runnable: runnable}, nil
}

func (a *EinoAgent) Decide(ctx context.Context, input Context) Decision {
	decision, err := a.runnable.Invoke(ctx, input)
	if err == nil {
		return decision
	}
	return Decision{
		Intent:       IntentUnsupported,
		Confidence:   1,
		NextAction:   ActionReply,
		AgentReply:   "主控流程暂时无法完成判断，本次不会执行任何修改或生成操作，请稍后重试。",
		Reason:       "eino_main_agent_graph_failed",
		Runtime:      "graph_error",
		Orchestrator: "eino",
		Warning:      summarizeError(err),
	}
}
