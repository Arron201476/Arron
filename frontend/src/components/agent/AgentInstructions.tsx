import { useEffect, useRef, useState } from "react";
import { ArrowLeft, LoaderCircle, RefreshCw, Save, Trash2 } from "lucide-react";
import { api, ApiError } from "../../api";
import { createUUID } from "../../uuid";
import type { AgentInstructionDocument, AgentInstructionScope, AgentInstructionView, Project } from "../../types";
import "./agentInstructions.css";

const labels: Record<AgentInstructionScope, string> = { user: "个人", workspace: "工作区", project: "作品" };

export function AgentInstructions({ workspaceName, initialProjectID = "" }: { workspaceName: string; initialProjectID?: string }) {
  const [projectID, setProjectID] = useState(initialProjectID);
  const [projects, setProjects] = useState<Project[]>([]);
  const [scope, setScope] = useState<AgentInstructionScope>(initialProjectID ? "project" : "user");
  const [view, setView] = useState<AgentInstructionView | null>(null);
  const [loading, setLoading] = useState(true);
  const [failure, setFailure] = useState("");
  const [busy, setBusy] = useState(false);
  const [dirty, setDirty] = useState(false);
  const sequence = useRef(0);
  const load = async () => {
    const current = ++sequence.current;
    setLoading(true); setFailure("");
    try {
      const [rules, items] = await Promise.all([api.getAgentInstructions(projectID), api.listProjects()]);
      if (current !== sequence.current) return;
      setView(rules); setProjects(items); setDirty(false);
    } catch (error) { if (current === sequence.current) { setView(null); setFailure(error instanceof Error ? error.message : "规则读取失败。"); } }
    finally { if (current === sequence.current) setLoading(false); }
  };
  useEffect(() => { void load(); return () => { sequence.current++; }; }, [projectID]);
  useEffect(() => {
    const beforeUnload = (event: BeforeUnloadEvent) => { if (dirty || busy) { event.preventDefault(); event.returnValue = ""; } };
    window.addEventListener("beforeunload", beforeUnload);
    return () => window.removeEventListener("beforeunload", beforeUnload);
  }, [dirty, busy]);
  const canLeave = () => !dirty || window.confirm("放弃尚未保存的规则修改？");
  const selected = view?.documents.find((item) => item.scope === scope);
  return <div className="instructions-page">
    <header className="instructions-header">
      <a className="back-button" href={initialProjectID ? `/projects/${encodeURIComponent(initialProjectID)}` : "/"} onClick={(event) => { if (busy || !canLeave()) event.preventDefault(); }}><ArrowLeft size={16} />{initialProjectID ? "作品" : "作品列表"}</a>
      <div><h1>Agent 规则</h1><span>{workspaceName}</span></div>
      <button className="icon-button" title="刷新规则" aria-label="刷新规则" disabled={busy || loading} onClick={() => { if (canLeave()) void load(); }}><RefreshCw size={17} /></button>
    </header>
    <main className="instructions-main">
      <div className="instructions-scope-bar">
        <div role="tablist" aria-label="规则范围">{(["user", "workspace", "project"] as const).map((item) => <button key={item} role="tab" aria-selected={scope === item} disabled={busy} onClick={() => { if (scope !== item && canLeave()) { setScope(item); setDirty(false); } }}>{labels[item]}</button>)}</div>
        {scope === "project" && <label>作品<select aria-label="规则作品" disabled={busy || loading} value={projectID} onChange={(event) => { if (canLeave()) { setProjectID(event.target.value); setDirty(false); } }}><option value="">选择作品</option>{projects.map((project) => <option key={project.project_id} value={project.project_id}>{project.title}</option>)}</select></label>}
      </div>
      {failure && <p className="form-error" role="alert">{failure}</p>}
      {loading ? <p role="status"><LoaderCircle className="spin" size={17} />正在读取规则</p> : selected && view ? <InstructionEditor key={`${view.workspace_id}:${projectID}:${scope}:${selected.version}`} document={selected} projectID={projectID} onDirty={setDirty} onBusy={setBusy} onSaved={(document) => { setView((current) => current ? { ...current, documents: current.documents.map((item) => item.scope === document.scope ? document : item) } : current); setDirty(false); }} /> : !failure && <p role="status">{scope === "project" ? "未选择作品" : "暂无规则"}</p>}
    </main>
  </div>;
}

function InstructionEditor({ document, projectID, onDirty, onBusy, onSaved }: { document: AgentInstructionDocument; projectID: string; onDirty: (value: boolean) => void; onBusy: (value: boolean) => void; onSaved: (document: AgentInstructionDocument) => void }) {
  const [content, setContent] = useState(document.content);
  const [enabled, setEnabled] = useState(document.enabled);
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState("");
  const [conflict, setConflict] = useState(false);
  const [receipt, setReceipt] = useState("");
  const request = useRef<{ payload: string; id: string } | null>(null);
  const bytes = new TextEncoder().encode(content).length;
  const dirty = content !== document.content || enabled !== document.enabled;
  useEffect(() => { onDirty(dirty); }, [dirty, onDirty]);
  const save = async (clear = false) => {
    if (pending || !document.can_edit || conflict) return;
    if (clear && !window.confirm("清空并停用此范围规则？后续新执行不再使用；历史执行及其冻结版本仍保留。")) return;
    setPending(true); onBusy(true); setFailure(""); setReceipt("");
    try {
      const command = { scope: document.scope, ...(projectID ? { project_id: projectID } : {}),
        expected_version: document.version, content: clear ? "" : content, enabled: clear ? false : enabled };
      const payload = JSON.stringify(command);
      if (request.current?.payload !== payload) request.current = { payload, id: createUUID() };
      const result = await api.updateAgentInstructions({ ...command, request_id: request.current.id });
      request.current = null;
      setContent(result.content); setEnabled(result.enabled); onSaved(result);
      setReceipt(`已保存 v${result.version}`);
    } catch (error) {
      setFailure(error instanceof Error ? error.message : "规则保存失败。");
      if (error instanceof ApiError && error.status === 409) setConflict(true);
    } finally { setPending(false); onBusy(false); }
  };
  return <section className="instruction-editor" aria-label={`${labels[document.scope]}规则`}>
    <div className="instruction-editor-heading"><h2>{labels[document.scope]}规则</h2><span>{document.version ? `v${document.version}` : "未保存"} · {document.enabled ? "已启用" : "已停用"}</span></div>
    <dl className="instruction-facts"><div><dt>可见范围</dt><dd>{document.scope === "user" ? "仅本人" : "工作区成员"}</dd></div><div><dt>生效范围</dt><dd>后续新执行</dd></div><div><dt>已暂停执行</dt><dd>保留原版本</dd></div></dl>
    <label className="instruction-content-label" htmlFor="instruction-content">规则内容</label>
    <textarea id="instruction-content" value={content} readOnly={!document.can_edit || pending || conflict} spellCheck={false} onChange={(event) => { setContent(event.target.value); setReceipt(""); }} aria-describedby="instruction-byte-count" />
    <div className="instruction-editor-options"><label><input type="checkbox" checked={enabled} disabled={!document.can_edit || pending || conflict} onChange={(event) => { setEnabled(event.target.checked); setReceipt(""); }} />启用</label><span id="instruction-byte-count" className={bytes > 8192 ? "form-error" : ""}>{bytes} / 8192 字节</span></div>
    {failure && <p className="form-error" role="alert">{failure}{conflict ? " 当前草稿未覆盖服务器版本，请刷新后核对。" : ""}</p>}
    {receipt && <p role="status">{receipt}</p>}
    <footer className="instruction-actions">{document.can_edit ? <><button className="primary-button" disabled={pending || conflict || !dirty || bytes > 8192 || (enabled && !content.trim()) || content.includes("\0")} onClick={() => void save()}>{pending ? <LoaderCircle size={15} className="spin" /> : <Save size={15} />}保存规则</button><button className="destructive-outline" disabled={pending || conflict || (!document.content && !document.enabled)} onClick={() => void save(true)}><Trash2 size={15} />清空并停用</button></> : <span>只读</span>}</footer>
  </section>;
}
