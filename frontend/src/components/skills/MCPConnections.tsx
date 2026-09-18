import { useEffect, useRef, useState } from "react";
import { KeyRound, RefreshCw, Save, Trash2, X } from "lucide-react";
import { api, ApiError } from "../../api";
import { createUUID } from "../../uuid";
import type { MCPConnectionInventory, MCPConnectionOption, MCPConnectionScope } from "../../types";

const errorMessage = (error: unknown) => error instanceof ApiError ? error.message : "连接凭据读取或更新失败。";

export function MCPConnections({ onChange }: { onChange?: () => Promise<void> }) {
  const [inventory, setInventory] = useState<MCPConnectionInventory | null>(null);
  const [failure, setFailure] = useState("");
  const [busy, setBusy] = useState(false);
  const generation = useRef(0);
  const load = async () => {
    const current = ++generation.current;
    const value = await api.listMCPConnections();
    if (current === generation.current) setInventory(value);
  };
  useEffect(() => {
    let active = true;
    void load().catch((error) => { if (active) setFailure(errorMessage(error)); });
    return () => { active = false; generation.current++; };
  }, []);
  return <div className="mcp-connections" aria-label="MCP 连接凭据">
    <header><h3><KeyRound size={15} />连接凭据</h3><button type="button" className="icon-button" title="刷新连接凭据" aria-label="刷新连接凭据" disabled={busy} onClick={() => {
      setBusy(true); setFailure("");
      void load().catch((error) => setFailure(errorMessage(error))).finally(() => setBusy(false));
    }}><RefreshCw size={15} /></button></header>
    {failure && <p className="form-error" role="alert">{failure}</p>}
    {!inventory && !failure && <p role="status">正在读取连接凭据…</p>}
    {inventory && !inventory.storage_available && <p className="form-error" role="status">凭据加密存储未配置，暂不能保存新凭据。</p>}
    {inventory?.items.length === 0 && <p className="muted">没有开放凭据配置的 MCP 连接。</p>}
    {inventory?.items.map((option) => <Connection key={option.server_id} option={option} inventory={inventory} busy={busy}
      onSave={async (scope, values, remove) => {
        if (busy) return;
        setBusy(true); setFailure("");
        try {
          const state = scope === "user" ? option.personal : option.workspace;
          await api.updateMCPConnection({ server_id: option.server_id, scope, expected_version: state.version, request_id: createUUID(),
            ...(remove ? { delete: true } : { values }) });
          await load();
          await onChange?.();
        } catch (error) {
          setFailure(errorMessage(error));
          try { await load(); } catch { /* Keep the original actionable error. */ }
        } finally { setBusy(false); }
      }} />)}
  </div>;
}

function Connection({ option, inventory, busy, onSave }: {
  option: MCPConnectionOption; inventory: MCPConnectionInventory; busy: boolean;
  onSave: (scope: MCPConnectionScope, values: Record<string, string>, remove: boolean) => Promise<void>;
}) {
  const [scope, setScope] = useState<MCPConnectionScope>("user");
  const [draft, setDraft] = useState<{ binding: string; values: Record<string, string> } | null>(null);
  const state = scope === "user" ? option.personal : option.workspace;
  const canManage = scope === "user" ? inventory.can_manage_personal : inventory.can_manage_workspace;
  const binding = JSON.stringify([scope, option.personal.version, option.workspace.version, option.fields, canManage, inventory.storage_available]);
  const editing = draft?.binding === binding;
  const values = editing ? draft.values : {};
  useEffect(() => { setDraft((current) => current && current.binding !== binding ? null : current); }, [binding]);
  const ready = option.fields.every((field) => Boolean(values[field]?.trim()));
  const status = option.effective_scope === "user" ? "使用个人凭据" : option.effective_scope === "workspace" ? "使用工作区凭据" : "未配置凭据";
  return <div className="mcp-connection">
    <div className="mcp-connection-title"><strong>{option.display_name}</strong><span>{status}</span></div>
    <fieldset className="mcp-connection-scope" disabled={busy}><legend className="visually-hidden">{option.display_name}凭据范围</legend>
      {(["user", "workspace"] as const).map((value) => <label key={value}><input type="radio" name={`${option.server_id}-credential-scope`} value={value} checked={scope === value} onChange={() => { setScope(value); setDraft(null); }} />{value === "user" ? "个人" : "工作区"}</label>)}
    </fieldset>
    <div className="mcp-connection-state"><span>{state.status === "active" ? `已保存 · v${state.version}` : state.status === "deleted" ? "已撤销" : "尚未保存"}</span>
      <div className="mcp-connection-actions">
        {!editing && canManage && <button type="button" disabled={busy || !inventory.storage_available} onClick={() => setDraft({ binding, values: {} })}><KeyRound size={14} />{state.status === "active" ? "更新凭据" : "配置凭据"}</button>}
        {canManage && state.status === "active" && <button type="button" disabled={busy} onClick={() => {
          if (!window.confirm(`撤销 ${option.display_name} 的${scope === "user" ? "个人" : "工作区"}凭据？${scope === "user" && option.workspace.status === "active" ? "之后会使用工作区凭据。" : "依赖此凭据的新调用将不可用。"}已经发出的外部请求无法撤回。`)) return;
          setDraft(null); void onSave(scope, {}, true);
        }}><Trash2 size={14} />撤销凭据</button>}
      </div>
    </div>
    {editing && canManage && inventory.storage_available && <form autoComplete="off" onSubmit={(event) => {
      event.preventDefault();
      if (!ready || !canManage || busy) return;
      const submitted = values;
      setDraft(null);
      void onSave(scope, submitted, false);
    }}>
      {option.fields.map((field) => <label className="mcp-credential-field" key={field}><span>{field}</span><input type="password" autoComplete="new-password" spellCheck={false} required maxLength={4096} disabled={busy} value={values[field] ?? ""} onChange={(event) => { const value = event.target.value; setDraft((previous) => previous?.binding === binding ? { binding, values: { ...previous.values, [field]: value } } : previous); }} /></label>)}
      <div className="mcp-connection-actions"><button type="submit" disabled={!ready || busy}><Save size={14} />保存凭据</button><button type="button" disabled={busy} onClick={() => setDraft(null)}><X size={14} />取消</button></div>
    </form>}
  </div>;
}
