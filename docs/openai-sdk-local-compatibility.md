# OpenAI SDK 与网关兼容矩阵

- Endpoint: `https://api.example.com/v1`
- Model: `gpt-5p6-terra`
- openai-agents: `0.21.1`
- openai: `3.3.1`
- Live probe: `no`

| Capability | Layer | Status | HTTP | Latency | Reason / Evidence |
|---|---|---|---:|---:|---|
| responses.create | python_sdk | supported |  |  |  |
| responses.stream | python_sdk | supported |  |  |  |
| responses.compact | python_sdk | supported |  |  |  |
| skills.list | python_sdk | supported |  |  |  |
| session.responses_compaction | agents_sdk | supported |  |  |  |
| mcp.streamable_http | agents_sdk | supported |  |  |  |
| mcp.stdio | agents_sdk | supported |  |  |  |
| skills.metadata_mapping | application_adapter | supported |  |  |  |
| mcp.stdio_tool_call | local_sdk | supported |  | 1978 ms | tools=2, read_call_completed=true |

`unsupported` 表示当前 SDK 或网关端点不提供该能力；`blocked` 表示端点存在但请求被配置、策略或上游错误阻断；`not_tested` 不等同于支持。
