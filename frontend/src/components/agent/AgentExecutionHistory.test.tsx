// @vitest-environment jsdom
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { AgentExecutionHistory } from "./AgentExecutionHistory";
import type { AgentToolCall, AgentTurn } from "../../types";

afterEach(cleanup);

it("identifies durable program calls without labelling direct calls as programmatic", () => {
  const calls = [
    { agent_tool_call_id: "program-read", program_call_id: "program-1", tool_name: "program-read" },
    { agent_tool_call_id: "direct-read", tool_name: "direct-read" },
  ].map((call) => ({ ...call, status: "completed", tool_kind: "runtime_function" })) as AgentToolCall[];
  render(<AgentExecutionHistory turns={[]} calls={calls} />);
  expect(screen.getAllByText("程序化调用")).toHaveLength(1);
  expect(screen.getByText("程序化调用").getAttribute("title")).toBe("program-1");
  expect(screen.getByText("direct-read")).toBeTruthy();
});

it("groups subtask reads by durable parent and keeps orphaned reads visible", () => {
  const calls = [
    { agent_tool_call_id: "parent", tool_id: "runtime:delegate_subtask", arguments_summary: { title: "Review motives" }, tool_name: "delegate_subtask", status: "running" },
    { agent_tool_call_id: "child", parent_tool_call_id: "parent", tool_name: "inspect_artifact_version" },
    { agent_tool_call_id: "orphan", parent_tool_call_id: "missing", tool_name: "read_workspace_file" },
  ].map((call) => ({ status: "completed", tool_kind: "runtime_function", requested_at: "2026-09-08T00:00:00Z", ...call })) as AgentToolCall[];
  render(<AgentExecutionHistory turns={[]} calls={calls} />);
  expect(screen.getByText("Review motives")).toBeTruthy();
  const reads = screen.getByText("1 次子任务读取").closest("details")!;
  expect(within(reads).getByText("inspect_artifact_version")).toBeTruthy();
  expect(within(reads).queryByText("read_workspace_file")).toBeNull();
  expect(screen.getByText("read_workspace_file")).toBeTruthy();
  expect(screen.queryByText("子任务结果")).toBeNull();
});

it("shows the actionable native compaction blocker", () => {
  const turn = {
    agent_turn_id: "turn", status: "failed", request: { content: "Continue" },
    created_at: "2026-09-05T00:00:00Z", observation: { failure_stage: "session_compaction" },
    error_code: "NATIVE_COMPACTION_UNAVAILABLE", error_message: "原会话已保留，请修复网关的 Responses compact 协议后重试。",
  } as AgentTurn;
  render(<AgentExecutionHistory turns={[turn]} calls={[]} />);
  expect(screen.getByText("原生上下文压缩")).toBeTruthy();
  expect(screen.getByText(/原会话已保留/)).toBeTruthy();
});

it("separates workflow attempts, background tasks and conversation tools using durable ownership", () => {
  const turn = { agent_turn_id: "turn", status: "committed", request: { content: "Main request" }, created_at: "2026-09-05T00:00:00Z" } as AgentTurn;
  const calls = [
    { agent_tool_call_id: "one", tool_name: "workflow-first", agent_turn_id: "turn", execution: { mode: "stateful_workflow", attempt_id: "attempt-one", attempt_no: 1, run_id: "run", step_id: "review", capability_id: "review_skill" } },
    { agent_tool_call_id: "two", tool_name: "workflow-retry", execution: { mode: "stateful_workflow", attempt_id: "attempt-two", attempt_no: 2, run_id: "run", step_id: "review", capability_id: "review_skill" } },
    { agent_tool_call_id: "bg", tool_name: "background-call", execution: { mode: "background_task", attempt_id: "background-attempt", attempt_no: 1, agent_task_id: "background-task", capability_id: "research" } },
    { agent_tool_call_id: "main", tool_name: "conversation-call", execution: { mode: "conversation", agent_turn_id: "turn" } },
    { agent_tool_call_id: "legacy", tool_name: "legacy-call" },
  ].map((call) => ({ ...call, status: "completed", requested_at: "2026-09-05T00:00:00Z", tool_kind: "runtime_function" })) as AgentToolCall[];
  const { container } = render(<AgentExecutionHistory turns={[turn]} calls={calls} capabilityNames={{ review_skill: "故事评审", research: "资料调研" }} />);
  const main = screen.getByText("Main request").closest("details")!;
  expect(within(main).getByText("conversation-call")).toBeTruthy();
  expect(within(main).queryByText("workflow-first")).toBeNull();
  expect(container.querySelectorAll(".execution-turn")).toHaveLength(5);
  const workflows = screen.getAllByText("工作流 · 故事评审").map((title) => title.closest("details")!);
  expect(within(workflows[0]).getByText("attempt-one")).toBeTruthy();
  expect(within(workflows[0]).queryByText("workflow-retry")).toBeNull();
  expect(within(workflows[1]).getByText("第 2 次尝试")).toBeTruthy();
  expect(screen.getByText("后台任务 · 资料调研")).toBeTruthy();
  expect(screen.getByText("未关联的工具调用")).toBeTruthy();
});

it("preserves orphaned conversation origins when the turn is outside the loaded history", () => {
  const call = { agent_tool_call_id: "call", execution: { mode: "conversation", agent_turn_id: "older-turn" }, tool_name: "read", status: "completed" } as AgentToolCall;
  render(<AgentExecutionHistory turns={[]} calls={[call]} />);
  expect(screen.getByText("对话")).toBeTruthy();
  expect(screen.getByText("older-turn")).toBeTruthy();
  expect(screen.queryByText("未关联的工具调用")).toBeNull();
});

it("shows successful tools without approvals and persisted model usage", () => {
  const turn = {
    agent_turn_id: "turn", status: "committed", request: { content: "Review outline" },
    created_at: "2026-09-05T00:00:00Z", observation: { model_id: "configured-model", usage: { total_tokens: 1234 } },
  } as AgentTurn;
  const call = { agent_tool_call_id: "call", agent_turn_id: "turn", status: "completed", tool_name: "read_skill_resource", tool_kind: "runtime_function", approval: null } as AgentToolCall;
  render(<AgentExecutionHistory turns={[turn]} calls={[call]} />);
  expect(screen.getByText("read_skill_resource")).toBeTruthy();
  expect(screen.getByText("configured-model")).toBeTruthy();
  expect(screen.getByText("1,234")).toBeTruthy();
  expect(screen.getByText(/1 次工具调用/)).toBeTruthy();
});
