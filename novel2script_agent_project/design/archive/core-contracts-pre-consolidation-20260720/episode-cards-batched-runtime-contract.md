# 分集卡分批运行合同

更新时间：2026-07-16

## 1. 目标

小说链和非小说链的 `episode_cards` 统一采用：

```text
锁定确认版上游 -> 每 5 集顺序生成 -> 每批严格校验 -> 确定性合并 -> 生成一个分集卡产物 -> 用户确认
```

解决一次性生成多集时模型只返回部分集数、长请求不稳定、失败后从头重做和部分内容误保存的问题。

## 2. 用户可见边界

- 用户仍只看到一个“分集卡”流程节点。
- 批次是内部执行单元，不逐批弹确认卡。
- 可展示“已完成 5/14 集分集卡”等产品进度，不展示 task ID、cursor 或 Eino 节点。
- 全部批次校验并合并成功后，只生成一个 `episode_cards` artifact，并统一等待用户确认。

## 3. 权威输入

小说链锁定：

```text
story_bible artifact id/version
episode_split artifact id/version
generation_config version
```

非小说链锁定：

```text
material_bank artifact id/version
story_seed artifact id/version
series_blueprint artifact id/version
generation_config version
```

任一锁定版本变化，旧 checkpoint 不得继续使用。

## 4. 批次输入输出

默认每批 5 集，最后一批可以少于 5 集。14 集应形成 `1-5`、`6-10`、`11-14` 三批，严格串行。

模型每次只接收：

- 当前批次范围；
- run 级生成配置；
- 当前批次所需上游内容；
- 最近两集已完成分集卡，用于连续性承接；
- 用户对本次生成的补充说明。

模型必须返回：

```json
{
  "episodes": [],
  "continuity_delta": {},
  "batch_risks": []
}
```

`episodes` 必须完整覆盖当前批次，episode ID 升序、无缺失、无重复、无越界。模型不得重写此前已完成批次。

## 5. Checkpoint 与恢复

每批成功后写入 run metadata：

```json
{
  "episode_cards_progress": {
    "locked_inputs": {},
    "completed_episodes": [],
    "continuity_deltas": [],
    "global_risks": [],
    "next_episode_id": 11,
    "completed_count": 10,
    "target_count": 14
  }
}
```

第 `6-10` 集失败时保留 `1-5` 集 checkpoint；重试从 `6-10` 集开始，不重复调用第 1 批。运行时重载后遵循同一规则。

## 6. 最终合并

全部批次成功后，Go runtime 确定性完成：

- episode ID 覆盖 `1..N`；
- 批次内容按 episode ID 拼接；
- `continuity_delta` 的列表字段顺序合并；
- 汇总批次风险；
- 写入 `next_action=script_generate`；
- 创建一个 `pending_approval` 的 `episode_cards` artifact。

最终完整性未通过时不得创建 artifact。

## 7. 失败提示

模型少返回、漏集或集号错误时，失败事件必须包含：

```text
error_code = EPISODE_CARDS_INCOMPLETE
```

用户看到“模型返回的第 X-Y 集分集卡不完整，本批次未保存；已保留此前完成批次，可从当前批次重试”，不能表现为前端断线或笼统生成失败。

## 8. Eino 与 Go 分工

Eino 专用图：

```text
validate_cards_batch_input
-> invoke_cards_batch_model
-> validate_cards_batch_output
```

Eino 负责单批输入、模型调用和基础输出验证。Go runtime 负责批次顺序、锁定输入、checkpoint、完整集号校验、最终合并、artifact 持久化和用户确认，避免双状态源。

## 9. 验收

1. 小说 14 集按 `5+5+4` 调用并合并为一个产物；
2. 非小说 6 集按 `5+1` 调用；
3. 第 2 批失败后只重试第 2 批；
4. 少返回一集时不保存该批，也不创建部分 artifact；
5. 最终 episode ID 完整覆盖目标集数；
6. continuity 和风险按批合并；
7. 上游版本变化后 checkpoint 重置；
8. 最终只出现一次用户确认。
