import { useEffect, useRef, useState } from "react";
import { Brain, Plus, RefreshCw, Save, Trash2, X } from "lucide-react";
import { api, ApiError } from "../../api";
import { createUUID } from "../../uuid";
import { canonicalJSONStringify } from "../../contextTargeting";
import type { AgentMemoryDocument, AgentMemoryUpdate, Principal } from "../../types";
import "./projectMemory.css";
import { MemoryGenerations } from "./MemoryGenerations";
import { MemoryPreferences } from "./MemoryPreferences";

export function ProjectMemory({ projectID, principal }: { projectID: string; principal: Principal }) {
  const [open, setOpen] = useState(false);
  return <><button type="button" className="icon-button" title="项目记忆" aria-label="项目记忆" onClick={() => setOpen(true)}><Brain size={17} /></button>
    {open && <MemoryDialog key={JSON.stringify([projectID, principal.workspace_id, principal.user_id, principal.role])} projectID={projectID} principal={principal} onClose={() => setOpen(false)} />}</>;
}

function validateDocument(value: AgentMemoryDocument, projectID: string, userID: string) {
  if (!value || value.project_id !== projectID || value.user_id !== userID || !Number.isSafeInteger(value.version) || value.version < 0 ||
      typeof value.enabled !== "boolean" || typeof value.forgotten !== "boolean" || !value.files || typeof value.files !== "object" || Array.isArray(value.files) ||
      Object.values(value.files).some((body) => typeof body !== "string") || value.forgotten && (value.enabled || Object.keys(value.files).length)) {
    throw new ApiError("AGENT_MEMORY_RECEIPT_INVALID", "记忆回执与当前用户或作品不一致。", 502);
  }
}

function MemoryDialog({ projectID, principal, onClose }: { projectID: string; principal: Principal; onClose: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [document, setDocument] = useState<AgentMemoryDocument | null>(null);
  const [files, setFiles] = useState<Record<string, string>>({});
  const [selected, setSelected] = useState("memory_summary.md");
  const [newPath, setNewPath] = useState("");
  const [enabled, setEnabled] = useState(false);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [unknown, setUnknown] = useState(false);
  const [failure, setFailure] = useState("");
  const [reload, setReload] = useState(0);
  const [showGenerations, setShowGenerations] = useState(false);
  const [showPreferences, setShowPreferences] = useState(false);
  const pending = useRef<AgentMemoryUpdate | null>(null);
  const submitting = useRef(false);
  const mounted = useRef(true);
  const editable = principal.role !== "viewer";
  const dirty = Boolean(document && (enabled !== document.enabled || canonicalJSONStringify(files) !== canonicalJSONStringify(document.files)));
  const locked = loading || busy || unknown || !editable;
  useEffect(() => { mounted.current = true; dialog.current?.showModal(); return () => { mounted.current = false; dialog.current?.close(); }; }, []);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setFailure("");
    api.getAgentMemory(projectID, controller.signal).then((value) => {
      if (controller.signal.aborted) return;
      validateDocument(value, projectID, principal.user_id);
      setDocument(value); setFiles(value.files); setEnabled(value.enabled);
      setSelected(Object.hasOwn(value.files, "memory_summary.md") ? "memory_summary.md" : Object.keys(value.files)[0] ?? "");
    }).catch((error) => { if (!controller.signal.aborted) { setDocument(null); setFailure(error instanceof Error ? error.message : "读取失败。"); } })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [projectID, principal.user_id, reload]);
  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => { if (dirty || unknown || busy) event.preventDefault(); };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty, unknown, busy]);
  const close = () => { if (!busy && !unknown && (!dirty || window.confirm("放弃未保存的记忆修改？"))) onClose(); };
  const save = async (forget = false) => {
    if (submitting.current || !document || !editable) return;
    if (!pending.current && forget && !window.confirm("遗忘本作品中属于你的全部记忆及历史正文？")) return;
    const command = pending.current ?? { project_id: projectID, expected_version: document.version, files: forget ? {} : structuredClone(files), enabled: forget ? false : enabled, forget, request_id: createUUID() };
    pending.current = command; submitting.current = true; setBusy(true); setFailure("");
    try {
      const value = await api.updateAgentMemory(command);
      if (!mounted.current) return;
      validateDocument(value, projectID, principal.user_id);
      if (value.request_id !== command.request_id || value.version !== command.expected_version + 1 ||
          !value.forgotten && (value.enabled !== command.enabled || canonicalJSONStringify(value.files) !== canonicalJSONStringify(command.files)) || command.forget && !value.forgotten) {
        throw new ApiError("AGENT_MEMORY_RECEIPT_INVALID", "记忆回执与本次提交不一致。", 502);
      }
      pending.current = null; setUnknown(false); setDocument(value); setFiles(value.files); setEnabled(value.enabled); setReload((value) => value + 1);
    } catch (error) {
      if (!mounted.current) return;
      setFailure(error instanceof Error ? error.message : "保存失败。");
      const definitive = error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS";
      if (definitive) pending.current = null;
      setUnknown(!definitive);
    } finally { submitting.current = false; if (mounted.current) setBusy(false); }
  };
  const paths = Object.keys(files).sort();
  return <dialog ref={dialog} className="project-memory-dialog" aria-label="项目记忆管理" onCancel={(event) => { event.preventDefault(); close(); }}>
    <header><div><h2>项目记忆</h2><small>仅本人 · {document ? `v${document.version}` : "读取中"}</small></div><button className="icon-button" title="关闭记忆" aria-label="关闭记忆" disabled={busy || unknown} onClick={close}><X size={17} /></button></header>
    <div className="memory-toolbar"><label>文件<select aria-label="记忆文件" value={selected} disabled={!document || busy} onChange={(event) => setSelected(event.target.value)}>{!paths.length && <option value="">暂无文件</option>}{paths.map((path) => <option key={path}>{path}</option>)}</select></label>
      <button className="icon-button" title="刷新记忆" aria-label="刷新记忆" disabled={loading || busy || unknown} onClick={() => { if (!dirty || window.confirm("放弃修改并读取最新记忆？")) setReload((value) => value + 1); }}><RefreshCw size={16} /></button></div>
    {loading && <p role="status">正在读取记忆</p>}
    {document && <><div className="memory-toolbar"><input aria-label="新增记忆文件路径" value={newPath} disabled={locked} onChange={(event) => setNewPath(event.target.value)} /><button className="icon-button" title="新增记忆文件" aria-label="新增记忆文件" disabled={locked || !newPath.trim() || Object.hasOwn(files, newPath.trim())} onClick={() => { const path = newPath.trim(); setFiles({ ...files, [path]: "" }); setSelected(path); setNewPath(""); }}><Plus size={16} /></button></div>
      {Object.hasOwn(files, selected) && <><textarea aria-label="记忆正文" spellCheck={false} value={files[selected]} readOnly={locked} onChange={(event) => setFiles({ ...files, [selected]: event.target.value })} /><button className="icon-button" title="移除当前文件" aria-label="移除当前文件" disabled={locked} onClick={() => { const next = { ...files }; delete next[selected]; setFiles(next); setSelected(Object.keys(next)[0] ?? ""); }}><Trash2 size={16} /></button></>}
      <label><input type="checkbox" checked={enabled} disabled={locked} onChange={(event) => setEnabled(event.target.checked)} />启用记忆</label>
      <footer><button className="primary-button" disabled={busy || loading || !editable || (!unknown && (!dirty || enabled && !files["memory_summary.md"]?.trim()))} onClick={() => void save()}><Save size={15} />{unknown ? "重试原请求" : "保存记忆"}</button><button className="destructive-outline" disabled={locked || !document.version || document.forgotten} onClick={() => void save(true)}><Trash2 size={15} />遗忘全部</button></footer></>}
    {failure && <p className="form-error" role="alert">{failure}</p>}
    <details onToggle={(event) => setShowGenerations(event.currentTarget.open)}><summary>生成任务</summary>{showGenerations && <MemoryGenerations projectID={projectID} userID={principal.user_id} editable={editable} />}</details>
    <details onToggle={(event) => setShowPreferences(event.currentTarget.open)}><summary>归档授权</summary>{showPreferences && <MemoryPreferences key={document?.version} projectID={projectID} userID={principal.user_id} editable={editable} />}</details>
  </dialog>;
}
