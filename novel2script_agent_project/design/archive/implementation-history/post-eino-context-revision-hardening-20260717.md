# Eino 接入后上下文与修改链路加固记录（2026-07-17）

## 1. 范围

本轮针对小说与非小说两条链路的共性问题加固，不为单个 artifact 写特例补丁。覆盖：

- Main Agent 意图识别、目标定位与回复保真；
- 控制模型和内容模型的上下文编译；
- 字段、实体、区块、集合、剧本选区与场景修改；
- 失败任务恢复与从失败游标重试；
- artifact 版本、下游影响确认和 run 状态同步；
- 模型调用追踪、错误脱敏和前端状态恢复。

## 2. 上下文编译合同

### 2.1 控制模型

- 最近对话最多 6 轮，单轮正文最多约 800 字；对话摘要最多约 1600 字。
- 事件摘要最多 8 条；已有 `event_digest` 时不再重复传完整事件。
- 作品附件只传轻量索引和最多约 400 字预览，不传 base64。
- artifact 列表只传轻量摘要；当前目标、当前步骤和选区保持结构化。

### 2.2 内容模型

- 局部修改不再重复传 `target_artifact`、历史对话、事件和完整源文。
- 剧本单句/多句修改只传目标场景附近最多前后 3 个 block，并保留稳定 line_id。
- 原文拆集等带 source offset 的目标，只传目标范围和必要边界窗口。
- 局部字段、实体、区块和集合修改的上下文预算统一受限；目标 artifact 只出现一次。
- 目标 episode_id 优先使用结构化 target，不依赖自然语言正则猜测。

## 3. 通用修改粒度

| 粒度 | 意图 | 运行时行为 |
|---|---|---|
| 标量字段 | `patch_artifact_field` | 只替换指定 field_path |
| 单个对象 | `patch_artifact_entity` | 只替换/追加/删除一个实体 |
| 列表结构 | `patch_artifact_collection` | 拆分、合并、插入、批量删除、移动 |
| 命名区块 | `patch_artifact_section` | 只替换指定区块 |
| 剧本选区 | `patch_script_span` | 只替换选中文字，保持行与结构锚点 |
| 剧本场景 | `regenerate_script_scene` | 只替换目标 scene_id |
| 整份产物 | `regenerate_artifact` | 生成目标 artifact 新版本 |

集合修改的内容模型输出必须是单个结构操作：

```json
{
  "field_path": "episodes",
  "operation": "replace_range",
  "start_index": 1,
  "delete_count": 1,
  "items": [{}, {}],
  "move_to": 0
}
```

允许的 operation：`replace_range`、`insert_entities`、`delete_entities`、`move_entity`。运行时负责边界校验、只改目标区间和保持相邻项。对于 `episodes`，运行时确定性重排 `episode_id`，同步实际集数和 run 级 `generation_config.target_episode_count`，并递增配置版本。

## 4. 失败与下游处理

- 局部修改失败时恢复修改前的审批点，但把失败 task、revision 和 instruction 保存为 `last_failed_*`。
- 用户明确说“继续/重试/重新试”时，可从失败 task 继续，不要求整步或整条链路重跑。
- 用户询问失败原因不会自动执行重试。
- 当前 artifact 生成新版本后，已有下游内容不会立刻删除或失效。
- 只有存在下游内容时才出现“保留现有后续内容 / 重新生成受影响内容”选择。
- 选择保留后，下游 artifact 保持当前版本；选择重生成后才按依赖范围失效并继续生成。

## 5. 可观测性与用户错误

- LLM metadata trace 增加 `component`、`operation`、`project_id`、`run_id`、`artifact_type`、`episode_id`、`task_id`、批次和修正次数。
- 初稿与长度修正调用分别标记为 `write_script` 和 `write_script_length_correction`。
- 用户事件只显示安全错误码和产品化说明，不暴露模型 URL、节点路径、SQLite 路径或驱动原始错误。
- 模型超时明确显示“模型服务超时”，不会表现成前端断线。

## 6. 回归证据

### 自动测试

- Go：Main Agent、Eino adapter、worker、runtime、LLM client、HTTP API 定向与全包测试通过。
- React：11 个测试文件、38 项测试通过。
- 前端生产构建通过。
- 浏览器 smoke：1440×900、1024×768 均为 200，无 console error、response error 或页面溢出。

### 真实模型双链路

- 小说：`run_001332`，completed，1 集，7 类活跃产物，4 次用户产物确认，0 次 runtime retry。
- 非小说：`run_001391`，completed，1 集，8 类活跃产物，5 次用户产物确认，0 次 runtime retry。
- 两条链路均未向用户暴露 `script_context` 确认卡。

### 真实结构修改

在小说测试作品中执行“把原文拆集第 1 集拆成两集”：

- Main Agent：`patch_artifact_collection / episodes / collection`；
- 内容模型输入约 16124 字符，较修复前同类 76627 字符明显降低；
- `episode_split` v1 superseded，v2 pending/confirmed，集数 1→2；
- run 级生成配置目标集数 1→2，配置版本 1→2；
- 原有 episode_cards、script_unit、scripts 保留；
- 出现统一下游处理确认，选择保留后 run 回到 completed。

## 7. 当前结论

Eino 与现有 Go 状态机职责边界保持清晰：Eino 负责节点编排和模型调用，Go Runtime 负责业务状态、版本、审批、失败游标和持久化。当前修改链路不再依赖某个 artifact 的专用补救逻辑，小说与非小说共用同一套上下文、集合操作、版本和下游确认合同。
