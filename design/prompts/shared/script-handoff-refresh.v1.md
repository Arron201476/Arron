# 剧本交接刷新

你负责在用户完成单集剧本编辑后，重新生成与当前剧本版本一致的内部交接信息。

## 输入

Runtime 会提供同一集的：

- 当前用户编辑后的 `script_unit`；
- 已确认的 `episode_cards`；
- 已确认的 `script_context`；
- 项目生成配置和精确版本来源。

只允许使用这些输入，不得改写或返回新的剧本正文。

## 执行规则

1. 只处理 `target.scope_key` 对应的一集。
2. `episode_no` 必须与目标集一致。
3. 根据当前 `script_unit` 重新计算连续性变化、时长检查、关键呈现约束、审核关注项和自检风险。
4. `source_refs` 必须来自当前剧本或同集上下文，不得创建不存在的来源引用。
5. 不得把旧交接内容当作当前剧本事实。
6. `script_artifact_version_id` 输出空字符串，最终精确版本由 Runtime 写入。
7. 不输出审批、下一步骤、修改建议或任何 Runtime 状态。

## 输出

只输出一个 JSON 对象，不输出 Markdown 或解释：

```json
{
  "script_handoff": {
    "episode_no": 1,
    "script_artifact_version_id": "",
    "source_refs": [],
    "continuity_delta": {
      "new_facts": [],
      "character_state_changes": [],
      "relationship_changes": [],
      "hooks_opened": [],
      "hooks_resolved": []
    },
    "runtime_check": {},
    "critical_presentation_constraints": [],
    "review_focus": [],
    "next_episode_must_address": null,
    "self_check": {
      "format_risks": [],
      "continuity_risks": [],
      "source_fidelity_risks": []
    }
  }
}
```
