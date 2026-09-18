# Runtime 状态到 API 落地清单

状态：Current。实现来源为 `backend/internal/agent/runtime`、`backend/cmd/server/routes.go` 和前端 API client。

## 用户动作映射

| 用户动作 | 入口 | 结果 |
|---|---|---|
| 闲聊、问状态 | 作品 message | 只追加聊天，不创建 run |
| 上传附件 | project files | 只保存 FileRef，不启动 run |
| 分析指定附件 | 作品 message | Main Agent `inspect_source`，内容模型只读目标文件并直接回复 |
| 明确生成但缺配置 | 作品 message | 持久化配置确认状态，不创建 run |
| 配置完整并生成 | 作品 message | 创建异步 run 和 `source_input` |
| 确认当前产物 | approval resolve `approve` | 确认当前版本，推进下一个 artifact |
| 修改当前产物 | 作品 message 或 artifact PATCH | 创建新版本；已有下游先标记 stale |
| 保留下游 | approval resolve `keep_downstream` | 保留旧下游，不覆盖 |
| 重生成下游 | approval resolve `regenerate_downstream` | invalidated 受影响旧产物，从最早受影响节点继续 |
| 运行中暂停 | run pause 或 Main Agent | 停在 task cursor，保留已完成内容 |
| 用户暂停后恢复 | run resume 或 Main Agent | 从 cursor 继续；不能越过 pending approval |
| 失败任务重试 | failed step rerun | 只允许失败 step，从失败 task cursor 继续 |
| 剧本局部修改 | 带 SelectionContext 的作品 message | 句子/多行/场景/单集按定位粒度修改，不使用 suggestion 中间态 |

## 两条流程

小说链：

```text
source_input
-> story_bible [确认]
-> episode_split [确认]
-> episode_cards [确认]
-> script_context [内部]
-> script_unit 1..N [逐集确认]
-> scripts [聚合]
-> completed
```

非小说链：

```text
source_input
-> material_bank [确认]
-> story_seed [确认]
-> series_blueprint [确认]
-> episode_cards [确认]
-> script_context [内部]
-> script_unit 1..N [逐集确认]
-> scripts [聚合]
-> completed
```

启动前确认的 `generation_config` 是 run 级权威配置。`episode_split`、`series_blueprint` 和 `episode_cards` 必须与目标集数一致，episode ID 必须完整覆盖 `1..N`。

## 修改传播

| 修改节点 | 可能受影响下游 |
|---|---|
| source_input | 当前模式下全部后续产物 |
| story_bible | episode_split、episode_cards、script_context、script_unit、scripts |
| episode_split | episode_cards、script_context、script_unit、scripts |
| material_bank | story_seed、series_blueprint、episode_cards、script_context、script_unit、scripts |
| story_seed | series_blueprint、episode_cards、script_context、script_unit、scripts |
| series_blueprint | episode_cards、script_context、script_unit、scripts |
| episode_cards | script_context、相关 script_unit、scripts |
| 单个 script_unit | scripts；连续性变化时可影响后续集 |

修改不会立即删除下游。只有实际存在受影响下游时才出现后续处理确认；没有下游时，未来步骤直接读取新版本。

## 状态规则

- `pending_approval`：新产物已生成，等待用户确认。
- `confirmed`：当前可作为下游输入的版本。
- `stale`：上游已变，但仍展示并等待用户决定是否重生成。
- `superseded`：同一产物已有更高版本。
- `invalidated`：用户已选择重生成，旧下游不再进入 Worker 上下文。

每次状态变化通过 run 快照持久化到 SQLite。前端刷新时同时恢复作品、聊天、附件、run、approval、events、artifacts、当前视图和未消费选区。若 workspace 指向已不存在的 active run，启动恢复会清理失效引用，保留作品聊天与附件。

## 失败与重连

- Worker 调用前写入 active task 和 cursor。
- `step_failed` 记录失败 step 与 task cursor。
- rerun 只接受该失败 step，成功后继续剩余 task。
- SSE 断线由浏览器自动重连，run 快照是最终事实来源。
- 未知 SSE cursor 触发全量重放，前端按 event ID 去重。

### 原文拆集检查点

`episode_split` 内部按“全局骨架 + 每批 5 集 + 最终覆盖检查”串行执行。每批成功后，Run metadata 持久化：

```json
{
  "episode_split_progress": {
    "locked_inputs": {},
    "global_plan": {},
    "completed_episodes": [],
    "next_episode_id": 11,
    "completed_count": 10,
    "target_count": 30
  }
}
```

重试只从失败批次继续。若 `source_input`、`story_bible` 或 `generation_config` 的锁定版本变化，旧检查点作废并重新规划。内部进度不创建额外 artifact，最终仍只有一个 `episode_split` 等待确认。

## Eino 边界

当前默认 runtime 为 `eino`。Main Agent 决策和内容模型任务都通过启动时编译的 Eino Graph 执行；`N2S_RUNTIME=native` 保留为回滚路径。Eino 不写 HTTP、Project、Run、Artifact、Approval 或 Event，仍由现有 SQLite Runtime 保持本文件定义的 API、状态、版本、确认和失败恢复语义。
