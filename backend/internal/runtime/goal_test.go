package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
)

func TestProjectGoalLifecycleIsCommittedWithMessageExchange(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "Goal 生命周期")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	setGoal := &agentcontract.GoalUpdate{
		Action: "set", Title: "完成三集希腊神话短剧",
		SuccessCriteria: []string{"三集剧本均已生成", "最终版本通过确认"},
	}
	first := createGoalExchange(t, ctx, store, project, "建立目标", setGoal)
	if first.Goal == nil || first.Goal.Status != "active" || first.Goal.Version != 1 {
		t.Fatalf("created goal = %+v", first.Goal)
	}
	active, err := store.GetActiveProjectGoal(ctx, project.ProjectID)
	if err != nil || active == nil || active.GoalID != first.Goal.GoalID {
		t.Fatalf("active goal = %+v, error = %v", active, err)
	}

	duplicate := createGoalExchange(t, ctx, store, project, "目标不变", setGoal)
	if duplicate.Goal == nil || duplicate.Goal.GoalID != first.Goal.GoalID {
		t.Fatalf("duplicate goal = %+v", duplicate.Goal)
	}
	var goalCount int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM project_goals WHERE project_id = ?`, project.ProjectID,
	).Scan(&goalCount); err != nil || goalCount != 1 {
		t.Fatalf("goal count = %d, error = %v", goalCount, err)
	}

	completed := createGoalExchange(t, ctx, store, project, "完成目标", &agentcontract.GoalUpdate{Action: "complete"})
	if completed.Goal == nil || completed.Goal.Status != "completed" || completed.Goal.Version != 2 {
		t.Fatalf("completed goal = %+v", completed.Goal)
	}
	active, err = store.GetActiveProjectGoal(ctx, project.ProjectID)
	if err != nil || active != nil {
		t.Fatalf("active goal after completion = %+v, error = %v", active, err)
	}
}

func TestProjectGoalFailureRollsBackMessageExchange(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "Goal 原子性")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	_, err = store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request:        agentcontract.MessageRequest{Content: "完成目标"},
		Decision: agentcontract.AgentDecision{
			Reply: "目标已完成。", Intent: "chat", Confidence: 1,
			GoalUpdate: &agentcontract.GoalUpdate{Action: "complete"},
		},
	})
	if err == nil {
		t.Fatal("CreateMessageExchange() error = nil, want missing goal rejection")
	}
	messages, listErr := store.ListMessages(ctx, project.PrimaryConversationID)
	if listErr != nil || len(messages) != 0 {
		t.Fatalf("messages after rollback = %+v, error = %v", messages, listErr)
	}
}

func createGoalExchange(
	t *testing.T,
	ctx context.Context,
	store *Store,
	project Project,
	content string,
	update *agentcontract.GoalUpdate,
) MessageExchange {
	t.Helper()
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request:        agentcontract.MessageRequest{Content: content},
		Decision: agentcontract.AgentDecision{
			Reply: "已更新项目目标。", Intent: "chat", Confidence: 1, GoalUpdate: update,
		},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	return exchange
}
