import type { Message } from "./types";

const legacyVideoBatchReply = "已收到视频材料。请确认本批次顺序和集号，然后在配置卡开始解析。";
const confirmedVideoBatchReply = "视频批次顺序和集号已确认，正在准备按集解析。";

export function presentConversationMessages(messages: Message[]): Message[] {
  return messages.map((message, index) => {
    if (message.role !== "assistant" || message.content !== legacyVideoBatchReply) return message;
    const previous = messages[index - 1];
    if (previous?.role !== "user" || !previous.message_context?.client_context?.current_asset_set_version_id) return message;
    return { ...message, content: confirmedVideoBatchReply };
  });
}
