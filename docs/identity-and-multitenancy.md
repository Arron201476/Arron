# 身份、授权与多租户运行约束

## 运行模式

- 仅监听 `127.0.0.1`、`[::1]` 或 `localhost` 时，可以不配置认证文件。Runtime 使用固定的本地 owner 身份，兼容单机开发数据。
- 监听任何非回环地址时，必须设置 `CONTENT_AGENT_AUTH_CONFIG_PATH`。缺失或无效时 Server 拒绝启动。
- 浏览器用 bearer token 调用 `POST /api/v1/auth/session` 换取 12 小时 HttpOnly、SameSite=Strict 会话 Cookie。访问令牌不写入浏览器存储。
- `CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN` 只代表 Sidecar 和受信任 worker 的服务身份。内部客户端必须显式携带该 Bearer；它可读 Agent 执行所需资源并调用 `/internal/v1/*`，不能调用最终用户写接口。

## 认证文件

配置文件只保存 token 的 SHA-256，不保存明文 token。参考 [auth.example.json](../backend/auth.example.json)。

PowerShell 生成摘要：

```powershell
$token = Read-Host 'Access token' -AsSecureString
$ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($token)
try {
  $plain = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr)
  [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($plain))).ToLowerInvariant()
} finally {
  [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr)
  $plain = $null
}
```

角色按最小权限递增：

| 角色 | 权限 |
|---|---|
| `viewer` | 读取当前工作区资源 |
| `editor` | viewer 权限及项目、消息、产物、Run 等业务写操作 |
| `admin` | editor 权限及 Skill、MCP 凭据引用管理 |
| `owner` | admin 权限及工作区删除 |

## 租户边界

- Principal 由认证中间件建立，客户端请求体中的 Actor 字段不被接受。
- Project 和 AgentTurn 直接记录 `workspace_id`/`user_id`；其他资源通过 Project、Run 或父资源解析工作区。
- 资源 ID 请求先解析所属工作区，不匹配统一返回 404，避免泄露资源是否存在。
- Skill 安装、活动目录、版本固定和 Registry 均按 workspace 隔离。同名、同 capability ID 的 Skill 可以存在于不同工作区，执行时按项目工作区解析。
- Sidecar 每次按 `project_id` 读取能力目录；最终用户身份不会转发给 Sidecar。

## 配额与审计

默认工作区配额：100 个项目、20 GiB 源资产与待上传预留、8 个活动 AgentTurn、100 个已安装 Skill。配额存放在 `workspace_quotas`，修改必须由运维控制面完成。

所有 HTTP 写请求、认证/授权拒绝和跨租户拒绝写入 `security_audit_events`。资产保留任务执行时同步按各工作区 `audit_retention_days` 清理过期审计，默认 90 天。

MCP 凭据表只接受 `env://NAME` 或 `vault://path`，不得存储实际 token、密码或 Header 内容。

## 删除传播

`DELETE /api/v1/workspace` 仅允许 owner 调用，请求体的 `confirmation` 必须精确等于当前 `workspace_id`。删除在一个数据库事务内完成：

- 取消活动 AgentTurn、AgentTask、AgentToolCall、Revision 和 Business Run；
- 软删除所有项目、Skill 安装、MCP 凭据和成员关系；
- 将源资产标记为删除并创建即时 retention job；
- 清除该工作区的活动 Skill 和不可变 Skill 包目录；
- 保留派生产物和安全审计作为留痕，后续由数据保留策略处理。

## 发布检查

1. 确认非回环监听配置了认证文件，且文件 ACL 仅允许运行账户读取。
2. 最终用户 token 与 Sidecar token 必须不同，并分别轮换。
3. 执行跨租户负例、角色矩阵、同名 Skill 和并发读取门禁。
4. 检查迁移备份和 `migration-reports`，再允许灰度。
5. 未经明确批准，不得上传、部署或重启远程测试机。
