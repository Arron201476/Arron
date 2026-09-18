from __future__ import annotations

from .models import AgentIntent, SourceMode

NOVEL_HINTS = ("第", "章", "小说", "原文", "穿越", "重生", "王爷", "女主", "男主")
NON_NOVEL_HINTS = ("灵感", "梗概", "短视频", "人设", "卖点", "素材", "想法", "设定")


def detect_intent(user_text: str, explicit_mode: SourceMode | None = None) -> AgentIntent:
    text = user_text.strip()
    if explicit_mode:
        return AgentIntent(source_mode=explicit_mode, action="generate", confidence=1.0, reason="用户显式指定入口")
    if not text:
        return AgentIntent(source_mode=None, action="unknown", confidence=0.0, reason="空输入")

    novel_score = sum(1 for hint in NOVEL_HINTS if hint in text)
    non_novel_score = sum(1 for hint in NON_NOVEL_HINTS if hint in text)
    if len(text) > 3000:
        novel_score += 2
    if "改" in text and len(text) < 1000:
        return AgentIntent(source_mode=None, action="revise", confidence=0.6, reason="短指令且包含修改意图")
    if novel_score >= non_novel_score and novel_score > 0:
        return AgentIntent(source_mode="novel", action="generate", confidence=0.7, reason="更像小说原文输入")
    if non_novel_score > 0:
        return AgentIntent(source_mode="non_novel", action="generate", confidence=0.7, reason="更像梗概/灵感/素材输入")
    return AgentIntent(source_mode="non_novel", action="generate", confidence=0.45, reason="无法确认时默认走非小说开发链")
