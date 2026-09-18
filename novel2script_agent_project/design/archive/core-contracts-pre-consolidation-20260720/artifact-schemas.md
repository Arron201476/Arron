# Artifact Schemas

本文档定义 Novel2Script Agent 的核心 artifact 结构。Artifact 是 Agent 步骤之间交接的稳定产物，不是 prompt 文本，也不是前端临时 UI 状态。

## 统一外壳

所有 artifact 都使用统一外壳，具体业务内容放在 `payload`。

```json
{
  "artifact_id": "artifact_01H...",
  "artifact_type": "story_bible",
  "project_id": "project_01H...",
  "run_id": "run_01H...",
  "version": 1,
  "status": "draft",
  "source_mode": "novel",
  "derived_from": [],
  "payload": {},
  "created_at": "2026-06-24T00:00:00Z",
  "updated_at": "2026-06-24T00:00:00Z"
}
```

## 状态枚举

```text
draft：Agent 生成或用户编辑后的草稿
pending_approval：等待用户确认
confirmed：用户确认后的稳定版本
superseded：已被新版本替代
invalidated：上游变更导致当前版本不再可信
failed：产物生成或校验失败，不能作为有效输入
```

## 核心 Artifact

### source_input

记录用户原始输入，小说和非小说共用。

```json
{
  "payload": {
    "source_mode": "novel | non_novel",
    "text": "",
    "files": [],
    "user_goal": "",
    "target_format": "小程序短剧",
    "constraints": [],
    "notes": []
  }
}
```

### story_bible

小说链的全文理解产物。

```json
{
  "payload": {
    "story_overview": {},
    "source_structure": [],
    "characters": [],
    "relationships": [],
    "world_rules": [],
    "major_plotline": [],
    "climax_map": {},
    "foreshadowing_and_payoff": [],
    "must_keep_facts": [],
    "short_drama_assets": {},
    "adaptation_risks": [],
    "source_trace": {}
  }
}
```

### episode_split

小说链拆集产物。`source_chunks` 不作为主 artifact，只作为 `source_refs` 或 `source_spans` 的内部来源引用。

```json
{
  "payload": {
    "target_episode_count": null,
    "actual_episode_count": 0,
    "split_strategy": "",
    "episodes": [
      {
        "episode_id": 1,
        "source_refs": [],
        "source_summary": "",
        "core_event": "",
        "character_turn": "",
        "boundary_reason": "",
        "hook_strength": "high | medium | low",
        "hook_type": "",
        "information_density": "high | medium | low",
        "pacing_risk": "none | weak_source_boundary | low_information_density | likely_padding",
        "requires_user_attention": false
      }
    ],
    "coverage_check": {},
    "global_risks": []
  }
}
```

### material_bank

非小说链素材库。

```json
{
  "payload": {
    "explicit_user_material": [],
    "constraints": [],
    "selling_points": [],
    "character_fragments": [],
    "plot_fragments": [],
    "dialogue_fragments": [],
    "style_references": [],
    "missing_information": [],
    "model_inference": []
  }
}
```

### story_seed

非小说链故事种子。

```json
{
  "payload": {
    "logline": "",
    "core_hook": "",
    "protagonist": {},
    "core_conflict": "",
    "emotional_engine": "",
    "relationship_engine": "",
    "payoff_promise": "",
    "risks": [],
    "source_trace": {}
  }
}
```

### series_blueprint

非小说链剧集蓝图。

```json
{
  "payload": {
    "target_episode_count": null,
    "series_structure": [],
    "phase_map": [],
    "major_turning_points": [],
    "character_arc_map": [],
    "payoff_distribution": [],
    "risk_flags": []
  }
}
```

### episode_cards

小说链和非小说链汇合后的分集卡。

```json
{
  "payload": {
    "episode_cards": [
      {
        "episode_id": 1,
        "title": "",
        "source_basis": [],
        "opening_pressure": "",
        "main_conflict": "",
        "scene_plan": [],
        "character_state": [],
        "relationship_state": [],
        "hook": "",
        "continuity_delta": {},
        "risk_notes": []
      }
    ]
  }
}
```

### script_context

公共剧本生成前的统一上下文。

```json
{
  "payload": {
    "source_mode": "novel | non_novel",
    "must_follow_facts": [],
    "allowed_additions": [],
    "forbidden_changes": [],
    "character_state": [],
    "relationship_state": [],
    "continuity_state": {},
    "source_material": {
      "text": "",
      "refs": [],
      "basis": []
    },
    "style_constraints": {},
    "user_notes": []
  }
}
```

### script_unit

单集剧本正文。

```json
{
  "payload": {
    "episode_id": 1,
    "title": "",
    "script_text": "",
    "script_document": {
      "episode_id": 1,
      "scenes": []
    },
    "source_refs": [],
    "source_basis": {
      "from_source_text": [],
      "from_story_bible": [],
      "from_story_seed": [],
      "from_series_blueprint": [],
      "generated_additions": []
    },
    "used_adaptation_suggestions": [],
    "used_generated_additions": [],
    "applied_visual_strategy": [],
    "pacing_execution_notes": [],
    "risk_notes": [],
    "continuity_delta": {},
    "source_trace": {},
    "self_check": {},
    "revision_notes": []
  }
}
```

## 版本规则

- Agent 或用户每次保存 artifact 都生成新版本。
- 用户确认后的版本标记为 `confirmed`。
- 新版本产生后，旧版本标记为 `superseded`，除非它仍被某个历史 run 引用。
- 上游 confirmed 版本变化后，下游相关 artifact 统一标记为 `invalidated`；影响轻重用 `dependency_invalidation.invalidation_level = review | regenerate_required` 表达，不新增额外复查状态。
- 前端默认展示最新版本，但必须允许查看历史版本和依赖来源。
## 审计补强：scripts 聚合产物

`script_unit` 是单集剧本正文，`scripts` 是用户最终查看、编辑和导出的剧本集合。主流程最终产物不能只停在单集 `script_unit`，必须有一个聚合 artifact 承接全剧。

```json
{
  "artifact_type": "scripts",
  "payload": {
    "source_mode": "novel | non_novel",
    "episode_count": 0,
    "script_units": [
      {
        "episode_id": 1,
        "script_unit_artifact_id": "",
        "version": 1,
        "status": "draft | pending_approval | confirmed | invalidated"
      }
    ],
    "script_document": {
      "artifact_id": "artifact_xxx",
      "version": 1,
      "source_mode": "novel | non_novel",
      "episodes": []
    },
    "editor_state": {
      "provider": "lexical",
      "document": {}
    },
    "suggestions": [],
    "comments": [],
    "global_continuity_state": {},
    "export_notes": [],
    "quality_flags": []
  }
}
```

字段说明：

- `script_document` 是业务可读结构，供前端、后端、导出和模型上下文共同使用。
- `editor_state` 保存编辑器内部状态；第一版 provider 固定为 `lexical`。它不能替代 `script_document`。
- `suggestions` 保存尚未处理或历史保留的 AI 修改建议。
- `comments` 保存用户批注 thread。
- `script_units` 保留单集来源引用，便于回溯每集生成结果。

## 统一 generation_config

`generation_config` 是启动生成前由 Main Agent 确认的配置，必须进入 `source_input.payload`，并由后续 artifact 逐步继承。它不是前端临时 UI 状态。

```json
{
  "generation_config": {
    "target_episode_count": 2,
    "episode_duration_minutes": 1.5,
    "target_script_chars": 500,
    "target_source_chars_per_episode": 0,
    "boundary_detection_window_chars": 800,
    "preserve_existing_episode_marks": false,
    "existing_episode_markers_detected": false
  }
}
```

字段规则：
- `target_episode_count`：用户确认的目标集数；不得静默默认 20。
- `episode_duration_minutes`：用户确认的单集时长；用于推导 `target_script_chars`。
- `target_script_chars`：单集剧本文字量目标；未填时可由时长按约 333 字/分钟推导。
- `target_source_chars_per_episode`：小说拆集可运行时动态计算；非必填。
- `boundary_detection_window_chars`：小说拆集边界检测窗口，默认 800。
- `preserve_existing_episode_marks`：是否优先保留原文已有分集标记。
- `existing_episode_markers_detected`：系统是否检测到原文已有分集/章节标记。

继承要求：
- `source_input` 必须保存 `generation_config`。
- 小说链：`story_bible` 可记录原文体量，`episode_split` 必须继承并输出边界检测，`episode_cards` 和 `scripts` 继续继承。
- 非小说链：`material_bank` 记录素材体量适配，`story_seed` 记录扩展策略，`series_blueprint` 决定 `resolved_episode_count`，`episode_cards` 按确认集数生成。
