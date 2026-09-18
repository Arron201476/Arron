# OpenAI SDK 与网关兼容矩阵

- Endpoint: `https://api.example.com/v1`
- Model: `gpt-5p6-terra`
- openai-agents: `0.21.1`
- openai: `3.7.0`
- Live probe: `yes`

| Capability | Layer | Status | HTTP | Latency | Reason / Evidence |
|---|---|---|---:|---:|---|
| responses.create | python_sdk | supported |  |  |  |
| responses.stream | python_sdk | supported |  |  |  |
| responses.compact | python_sdk | supported |  |  |  |
| skills.list | python_sdk | supported |  |  |  |
| session.responses_compaction | agents_sdk | supported |  |  |  |
| mcp.streamable_http | agents_sdk | supported |  |  |  |
| responses.create | gateway_live | supported |  | 4219 ms | response_status=completed |
| responses.stream | gateway_live | supported |  | 1890 ms | event=response.completed, event=response.content_part.added, event=response.content_part.done, event=response.created, event=response.in_progress, event=response.output_item.added, event=response.output_item.done, event=response.output_text.delta, event=response.output_text.done |
| responses.function_tool | gateway_live | supported |  | 2246 ms | function_calls=1 |
| responses.compact | gateway_live | supported |  | 7910 ms | object=response.compaction, output_items=1 |
| skills.list | gateway_live | unsupported | 404 | 26 ms | endpoint_unavailable; NotFoundError |
| mcp.remote_tool_call | gateway_live | not_tested |  |  | no_local_mcp_fixture |

`unsupported` 表示当前 SDK 或网关端点不提供该能力；`blocked` 表示端点存在但请求被配置、策略或上游错误阻断；`not_tested` 不等同于支持。
