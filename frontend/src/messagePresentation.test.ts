import { describe, expect, it } from "vitest";
import { presentConversationMessages } from "./messagePresentation";
import type { Message } from "./types";

const userMessage = (assetSetVersionID?: string): Message => ({
  message_id: "user-1",
  role: "user",
  content: "请解析视频",
  created_at: "2026-08-18T08:00:00Z",
  message_context: {
    attachment_refs: [],
    client_context: assetSetVersionID ? { current_asset_set_version_id: assetSetVersionID } : {},
  },
});

const assistantMessage: Message = {
  message_id: "assistant-1",
  role: "assistant",
  content: "已收到视频材料。请确认本批次顺序和集号，然后在配置卡开始解析。",
  created_at: "2026-08-18T08:00:01Z",
};

describe("presentConversationMessages", () => {
  it("shows confirmed copy when the submitted message carries a sealed batch version", () => {
    const presented = presentConversationMessages([userMessage("asv-1"), assistantMessage]);
    expect(presented[1].content).toBe("视频批次顺序和集号已确认，正在准备按集解析。");
  });

  it("keeps the confirmation request when no confirmed batch was submitted", () => {
    const presented = presentConversationMessages([userMessage(), assistantMessage]);
    expect(presented[1].content).toBe(assistantMessage.content);
  });
});
