import { useEffect, useRef, useState } from "react";
import { RefreshCw, Wrench } from "lucide-react";
import { api, ApiError } from "../../api";
import type { AgentToolConfiguration, AgentToolConfigurationOption, AgentToolDescriptor } from "../../types";
import { MCPConnections } from "./MCPConnections";

const toolNames: Record<string, string> = { web_search: "网页搜索", file_search: "文件搜索", code_interpreter: "代码解释器", image_generation: "图像生成", programmatic_tool_calling: "程序化工具调用" };
const toolGroups = [{ kind: "runtime_function", label: "项目工具" }, { kind: "mcp", label: "MCP" }, { kind: "hosted", label: "Hosted tools" }];
const errorMessage = (error: unknown) => error instanceof ApiError ? error.message : "工具配置读取或更新失败。";

export function AgentToolInventory({ canManage = false, onConfigurationChange }: { canManage?: boolean; onConfigurationChange?: () => Promise<void> }) {
  const [tools, setTools] = useState<AgentToolDescriptor[] | null>(null);
  const [configuration, setConfiguration] = useState<AgentToolConfiguration | null>(null);
  const [failure, setFailure] = useState("");
  const [busy, setBusy] = useState("");
  const [mcpOpen, setMCPOpen] = useState(false);
  const generation = useRef(0);
  const load = async () => {
    const current = ++generation.current;
    const [items, settings] = await Promise.all([
      api.listAgentTools(),
      api.getAgentToolConfiguration().catch((error: unknown) => {
        if (error instanceof ApiError && error.status === 404) return null;
        throw error;
      }),
    ]);
    if (generation.current !== current) return;
    setTools(items);
    setConfiguration(settings);
  };
  useEffect(() => {
    let active = true;
    void load().catch((error) => { if (active) setFailure(errorMessage(error)); });
    return () => { active = false; generation.current++; };
  }, []);
  const update = async (option: AgentToolConfigurationOption) => {
    if (!configuration || !canManage || busy || (!option.configurable && !option.enabled)) return;
    if (!option.enabled && !window.confirm(`启用 ${toolNames[option.name] ?? option.name} (${option.id})？后续任务可以调用该工具；写入和敏感操作仍需逐次授权。`)) return;
    setBusy(option.id); setFailure("");
    try {
      const next = await api.updateAgentToolConfiguration(configuration.version, { [option.id]: !option.enabled });
      setConfiguration(next);
      await load();
      await onConfigurationChange?.();
    } catch (error) {
      setFailure(errorMessage(error));
      try { await load(); } catch { /* Preserve the actionable update failure. */ }
    } finally { setBusy(""); }
  };
  return <section className="tool-inventory" aria-label="Agent 工具配置">
    <header className="tool-inventory-heading"><h2><Wrench size={16} />Agent 工具配置</h2><button className="icon-button" aria-label="刷新工具配置" title="刷新工具配置" disabled={Boolean(busy)} onClick={() => {
      setBusy("refresh"); setFailure("");
      void load().catch((error) => setFailure(errorMessage(error))).finally(() => setBusy(""));
    }}><RefreshCw size={15} className={busy === "refresh" ? "spin" : ""} /></button></header>
    {failure && <p className="form-error" role="alert">{failure}</p>}
    {tools === null ? !failure && <p role="status">正在读取工具配置…</p> : toolGroups.map(({ kind, label }) => {
      const items = tools.filter((tool) => tool.kind === kind);
      const options = configuration?.options.filter((option) => option.kind === kind) ?? [];
      const states = [...options, ...items.filter((tool) => !options.some((option) => option.id === tool.id))];
      return <details key={kind} onToggle={(event) => { if (kind === "mcp") setMCPOpen(event.currentTarget.open); }}><summary><strong>{label}</strong><span>{states.length ? `${states.filter((tool) => tool.enabled).length} / ${states.length} 已启用` : "未配置"}</span></summary>
        {options.map((option) => {
          const name = `${toolNames[option.name] ?? option.name}${options.filter((item) => item.name === option.name).length > 1 ? ` (${option.id})` : ""}`;
          const descriptor = items.find((item) => item.id === option.id);
          return <div className="tool-inventory-option" key={option.id}>
            <span><strong>{name}</strong><small>{option.transport ?? option.description}</small>{descriptor && <small>{descriptor.approval === "always" ? "逐次授权" : "无需逐次授权"} · {descriptor.access === "read" ? "读取" : descriptor.access === "write" ? "写入" : "敏感操作"}</small>}{option.user_message && <small className="form-error">{option.user_message}</small>}</span>
            <label className="tool-inventory-toggle"><input type="checkbox" role="switch" aria-label={`${name}启用状态`} checked={option.enabled} disabled={!canManage || Boolean(busy) || (!option.configurable && !option.enabled)} onChange={() => void update(option)} /><span>{option.enabled ? "已启用" : "已禁用"}</span></label>
          </div>;
        })}
        {items.filter((tool) => !options.some((option) => option.id === tool.id)).map((tool) => <div className="tool-inventory-row" key={tool.id}><span><strong>{tool.name}</strong><small>{tool.server_id || tool.description}</small></span><span>{tool.enabled ? "已启用" : "已禁用"}<small>{tool.approval === "always" ? "逐次授权" : "无需逐次授权"} · {tool.access === "read" ? "读取" : tool.access === "write" ? "写入" : "敏感操作"}</small></span></div>)}
        {kind === "mcp" && mcpOpen && <MCPConnections onChange={async () => { await load(); await onConfigurationChange?.(); }} />}
      </details>;
    })}
  </section>;
}
