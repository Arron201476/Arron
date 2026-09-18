// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { ProcessHistory, summarizeActivities, TimelineMessage } from "./AgentHistory";
import type { Asset, Message, ProjectActivity } from "../../types";

afterEach(cleanup);

describe("failure transition history", () => {
  it.each([
    ["prepared", "失败后续步骤待继续", "run.paused", "waiting"],
    ["blocked", "失败后续步骤无法继续", "run.failed", "failed"],
  ])("shows a %s branch without treating the failed step as successful", (state, label, runEvent, tone) => {
    const items = [
      activity("failed", "step.failed", "read_source", "2026-09-10T01:00:00Z"),
      activity("branch", `workflow.failure_transition_${state}`, "read_source", "2026-09-10T01:00:01Z"),
      activity("run", runEvent, null, "2026-09-10T01:00:02Z"),
    ];
    const { container } = render(<ProcessHistory activities={items} activeRunID="run-1" />);
    expect(screen.getByText((text) => text.startsWith(label))).toBeTruthy();
    expect(container.querySelector(`.process-events > .${tone}`)).not.toBeNull();
    expect(container.textContent).toContain("1 个失败");
    expect(container.textContent).not.toContain("1 个步骤已完成");
  });
});

describe("summarizeActivities", () => {
  it("keeps one latest user-facing state per step", () => {
    const items = [
      activity("1", "step.started", "read_source", "2026-08-11T01:00:00Z"),
      activity("2", "approval.requested", "read_source", "2026-08-11T01:01:00Z"),
      activity("3", "approval.resolved", "read_source", "2026-08-11T01:02:00Z"),
      activity("4", "step.completed", "read_source", "2026-08-11T01:03:00Z"),
      activity("5", "run.completed", null, "2026-08-11T01:04:00Z"),
    ];

    expect(summarizeActivities(items)).toEqual([items[3]]);
  });
});

describe("TimelineMessage", () => {
  it("does not claim cancellation when a submission result is unknown", () => {
    render(<TimelineMessage message={{ message_id: "local", role: "user", content: "Continue", created_at: "2026-09-09T00:00:00Z", delivery_status: "unconfirmed" }} />);
    expect(screen.getByText("发送结果未确认")).toBeTruthy();
    expect(screen.queryByText("本轮已停止，内容已保留")).toBeNull();
  });

  it("shows the safe server failure reason without hiding the original request", () => {
    render(<TimelineMessage message={{
      message_id: "failed", role: "user", content: "继续检查故事",
      created_at: "2026-09-06T00:00:00Z", delivery_status: "failed",
      delivery_error: "模型网关未返回有效的原生上下文压缩结果。原会话已保留。",
    }} />);
    expect(screen.getByRole("alert").textContent).toContain("原生上下文压缩");
    expect(screen.getByText("继续检查故事")).not.toBeNull();
  });

  it("shows the uploaded filename with the optimistic user message", () => {
    const message = {
      message_id: "local-1",
      role: "user",
      content: "把这个小说转成剧本",
      created_at: "2026-08-13T08:00:00Z",
      delivery_status: "sending",
      message_context: {
        attachment_refs: [{ asset_id: "asset-1", asset_snapshot_id: "snapshot-1" }],
      },
    } satisfies Message;
    const asset = {
      asset_id: "asset-1",
      current_snapshot_id: "snapshot-1",
      kind: "text",
      display_name: "测试用书.txt",
      original_filename: "测试用书.txt",
      status: "available",
      parse_status: "completed",
    } satisfies Asset;

    render(<TimelineMessage message={message} assets={[asset]} />);

    expect(screen.getByText("测试用书.txt")).not.toBeNull();
    expect(screen.getByText("正在发送")).not.toBeNull();
  });

  it("renders tool approval waiting as a stable state", () => {
    const message = {
      message_id: "agent-waiting-1",
      role: "assistant",
      content: "需要确认后才能继续。",
      created_at: "2026-08-13T08:00:00Z",
      delivery_status: "waiting_approval",
    } satisfies Message;

    const { container } = render(<TimelineMessage message={message} />);

    expect(screen.getAllByText("等待工具授权").length).toBeGreaterThan(0);
    expect(container.querySelector(".mini-spinner")).toBeNull();
    expect(container.querySelector(".lucide-clock-3")).not.toBeNull();
  });
});

function activity(eventID: string, eventType: string, stepID: string | null, occurredAt: string): ProjectActivity {
  return { event_id: eventID, event_type: eventType, run_id: "run-1", capability_id: "novel_to_script", step_id: stepID, occurred_at: occurredAt };
}
