import { describe, expect, it, vi } from "vitest";
import { createOptimisticUserMessage, markOptimisticMessageUnconfirmed, markOptimisticMessageFailed, mergeSnapshotMessages, settleOptimisticMessage } from "./optimisticMessage";
import type { AgentComposerContext } from "./types";

describe("optimistic messages", () => {
  it("does not use identical content or asset IDs as proof that a submission was received", () => {
    const pending = { message_id: "local_pending", role: "user", content: "Continue", created_at: "2026-09-04T00:00:01Z", delivery_status: "sending", message_context: { attachment_refs: [{ asset_id: "file", asset_snapshot_id: "new" }] } } as const;
    const another = { message_id: "saved_other", role: "user", content: "Continue", created_at: "2026-09-04T00:00:02Z", message_context: { attachment_refs: [{ asset_id: "file", asset_snapshot_id: "old" }] } } as const;
    expect(mergeSnapshotMessages([another], [pending])).toEqual([pending, another]);
  });

  it("uses durable message identity even if content or timestamps changed", () => {
    const current = { message_id: "saved", role: "user", content: "Previous", created_at: "2026-09-04T00:00:01Z", delivery_status: "running" } as const;
    const authoritative = { message_id: "saved", role: "user", content: "Revised", created_at: "2026-09-04T00:00:02Z" } as const;
    expect(mergeSnapshotMessages([authoritative], [current])).toEqual([authoritative]);
  });

  it("renders the submitted query immediately with its immutable selection", () => {
    vi.stubGlobal("crypto", { randomUUID: () => "request-1" });
    const context = {
      view: { project_id: "project-1", run_id: null, capability_id: null, artifact_id: "artifact-1", artifact_version_id: "version-1", artifact_type: "source_manifest", scope_key: "singleton", artifact_label: "来源清单" },
      selection: { schema_version: "1.0.0", artifact_id: "artifact-1", artifact_type: "source_manifest", target_scope: "selection", field_path: "units[0].summary", display: { artifact_label: "来源清单", selected_text_summary: "未写完的概要" } },
    } as AgentComposerContext;

    const message = createOptimisticUserMessage("自动补齐", context);

    expect(message).toMatchObject({ message_id: "local_request-1", role: "user", content: "自动补齐", delivery_status: "sending" });
    expect(message.message_context?.selection_snapshot?.selection.field_path).toBe("units[0].summary");
    vi.unstubAllGlobals();
  });

  it("preserves failed content in the timeline", () => {
    const failed = markOptimisticMessageFailed([{ message_id: "local-1", role: "user", content: "自动补齐", created_at: new Date().toISOString(), delivery_status: "sending" }], "local-1");
    expect(failed[0]).toMatchObject({ content: "自动补齐", delivery_status: "failed" });
  });

  it("preserves interrupted content without pretending the server confirmed cancellation", () => {
    const interrupted = markOptimisticMessageUnconfirmed([{ message_id: "local-1", role: "user", content: "自动补齐", created_at: new Date().toISOString(), delivery_status: "sending" }], "local-1");
    expect(interrupted[0]).toMatchObject({ content: "自动补齐", delivery_status: "unconfirmed" });
  });

  it("does not duplicate messages already delivered by the event stream", () => {
    const confirmed = [
      { message_id: "user-1", role: "user", content: "自动补齐", created_at: new Date().toISOString() },
      { message_id: "agent-1", role: "assistant", content: "已定位修改位置。", created_at: new Date().toISOString() },
    ];
    const settled = settleOptimisticMessage([
      { message_id: "local-1", role: "user", content: "自动补齐", created_at: new Date().toISOString(), delivery_status: "sending" },
      ...confirmed,
    ], "local-1", confirmed);
    expect(settled.map((message) => message.message_id)).toEqual(["user-1", "agent-1"]);
  });

  it("keeps an optimistic message when an event-stream refresh has no confirmed copy", () => {
    const pending = { message_id: "local-1", role: "user", content: "普通聊天", created_at: "2026-08-25T12:00:00Z", delivery_status: "sending" } as const;

    expect(mergeSnapshotMessages([], [pending])).toEqual([pending]);
  });

  it("requires an exact turn identity or direct receipt before settling equal snapshot text", () => {
    const pending = { message_id: "local-1", role: "user", content: "普通聊天", created_at: "2026-08-25T12:00:00Z", delivery_status: "sending" } as const;
    const confirmed = { message_id: "user-1", role: "user", content: "普通聊天", created_at: "2026-08-25T12:00:01Z" } as const;

    expect(mergeSnapshotMessages([confirmed], [pending])).toEqual([pending, confirmed]);
  });

  it("does not equate a slow starter handoff with another use of the same attachment", () => {
    const pending = {
      message_id: "local-1", role: "user", content: "解析视频", created_at: "2026-08-25T12:00:00Z", delivery_status: "sending",
      message_context: { attachment_refs: [{ asset_id: "asset-1", asset_snapshot_id: "snapshot-1" }] },
    } as const;
    const confirmed = {
      message_id: "user-1", role: "user", content: "解析视频", created_at: "2026-08-25T12:17:00Z",
      message_context: { attachment_refs: [{ asset_id: "asset-1", asset_snapshot_id: "snapshot-1" }] },
    } as const;

    expect(mergeSnapshotMessages([confirmed], [pending])).toEqual([pending, confirmed]);
  });

  it("does not settle against an older identical message or a different attachment", () => {
    const pending = {
      message_id: "local-1", role: "user", content: "解析视频", created_at: "2026-08-25T12:10:00Z", delivery_status: "sending",
      message_context: { attachment_refs: [{ asset_id: "asset-new", asset_snapshot_id: "snapshot-new" }] },
    } as const;
    const older = {
      message_id: "user-old", role: "user", content: "解析视频", created_at: "2026-08-25T12:00:00Z",
      message_context: { attachment_refs: [{ asset_id: "asset-new", asset_snapshot_id: "snapshot-new" }] },
    } as const;
    const different = {
      message_id: "user-other", role: "user", content: "解析视频", created_at: "2026-08-25T12:11:00Z",
      message_context: { attachment_refs: [{ asset_id: "asset-old", asset_snapshot_id: "snapshot-old" }] },
    } as const;

    expect(mergeSnapshotMessages([older, different], [pending])).toEqual([older, pending, different]);
  });

  it("does not duplicate the same cached optimistic message during stream refresh", () => {
    const pending = { message_id: "local-1", role: "user", content: "解析视频", created_at: "2026-08-25T12:00:00Z", delivery_status: "sending" } as const;

    expect(mergeSnapshotMessages([], [pending, pending])).toEqual([pending]);
  });
});
