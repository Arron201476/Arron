import { useEffect, useRef, useState } from "react";
import { ChevronDown, RefreshCw, Pause, Play, X } from "lucide-react";
import { api } from "../../api";
import { MemorySourcePicker } from "./MemorySourcePicker";

const statuses = { queued: "排队中", running: "执行中", paused: "已暂停", cancelled: "已取消", completed: "已完成", failed: "失败" };
const phases = { extraction: "提取", consolidation: "整合", publication: "发布" };
export interface MemoryGenerationSummary {
  generation_id: string; project_id: string; user_id: string; status: keyof typeof statuses;
  phase: keyof typeof phases; revision: number; attempt: number; model_id: string; error_code?: string; created_at: string;
  resume_available?: boolean;
}
export interface MemoryGenerationPage { items: MemoryGenerationSummary[]; next_cursor?: string }

function checkedPage(value: MemoryGenerationPage, projectID: string, userID: string) {
  if (!value || !Array.isArray(value.items) || value.items.length > 50 ||
      value.next_cursor !== undefined && (typeof value.next_cursor !== "string" || value.next_cursor.length > 256)) throw new Error("生成任务列表回执无效。");
  const ids = new Set<string>();
  for (const item of value.items) {
    if (!item || item.project_id !== projectID || item.user_id !== userID || typeof item.generation_id !== "string" || !item.generation_id ||
        ids.has(item.generation_id) || !Object.hasOwn(statuses, item.status) || !Object.hasOwn(phases, item.phase) ||
        !Number.isSafeInteger(item.revision) || item.revision < 1 || !Number.isSafeInteger(item.attempt) || item.attempt < 0 ||
        typeof item.model_id !== "string" || typeof item.created_at !== "string" || !Number.isFinite(Date.parse(item.created_at)) ||
        item.resume_available !== undefined && typeof item.resume_available !== "boolean" ||
        item.error_code !== undefined && typeof item.error_code !== "string") throw new Error("生成任务与当前用户或作品不一致。");
    ids.add(item.generation_id);
  }
  if (value.next_cursor && !ids.has(value.next_cursor)) throw new Error("生成任务分页回执无效。");
  return value;
}

export function MemoryGenerations({ projectID, userID, editable = false }: { projectID: string; userID: string; editable?: boolean }) {
  const [items, setItems] = useState<MemoryGenerationSummary[]>([]);
  const [cursor, setCursor] = useState("");
  const [next, setNext] = useState("");
  const [reload, setReload] = useState(0);
  const [loading, setLoading] = useState(true);
  const [failure, setFailure] = useState("");
  const [busy, setBusy] = useState(false);
  const [unknown, setUnknown] = useState(false);
  const [showSources, setShowSources] = useState(false);
  const submitting = useRef(false);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setFailure("");
    if (!cursor) { setItems([]); setNext(""); }
    api.listMemoryGenerations(projectID, cursor, controller.signal).then((raw) => {
      if (controller.signal.aborted) return;
      const page = checkedPage(raw, projectID, userID);
      if (page.next_cursor && page.next_cursor === cursor) throw new Error("生成任务分页未前进。");
      setItems((prior) => cursor ? [...prior.filter((item) => !page.items.some((row) => row.generation_id === item.generation_id)), ...page.items] : page.items);
      setNext(page.next_cursor ?? "");
      setUnknown(false);
    }).catch((error) => { if (!controller.signal.aborted) setFailure(error instanceof Error ? error.message : "读取生成任务失败。"); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [projectID, userID, cursor, reload]);
  const control = async (item: MemoryGenerationSummary, action: "pause" | "resume" | "cancel") => {
    if (!editable || submitting.current || unknown || loading) return;
    if (action === "resume" && item.resume_available !== true) return;
    if (action === "cancel" && !window.confirm("取消此记忆生成任务？")) return;
    submitting.current = true; setBusy(true); setFailure("");
    try {
      const receipt = await api.controlMemoryGeneration(projectID, item.generation_id, action, item.revision);
      if (!mounted.current) return;
      const requestedPause = action === "pause" && receipt?.status === "running" && receipt.error_code === "MEMORY_GENERATION_PAUSE_REQUESTED";
      const expected = requestedPause ? "running" : { pause: "paused", resume: "queued", cancel: "cancelled" }[action];
      if (!receipt || receipt.generation_id !== item.generation_id || receipt.project_id !== projectID || receipt.user_id !== userID ||
          receipt.phase !== item.phase || receipt.attempt !== item.attempt || receipt.model_id !== item.model_id ||
          receipt.revision !== item.revision + 1 || receipt.status !== expected) throw new Error("任务控制回执未确认，请刷新状态。");
      setCursor(""); setReload((value) => value + 1);
    } catch (error) {
      if (mounted.current) { setUnknown(true); setFailure(error instanceof Error ? error.message : "任务控制未确认，请刷新状态。"); }
    } finally { submitting.current = false; if (mounted.current) setBusy(false); }
  };
  return <section className="memory-generations" aria-label="记忆生成任务">
    {editable && <details onToggle={(event) => setShowSources(event.currentTarget.open)}><summary>从归档生成记忆</summary>{showSources && <MemorySourcePicker projectID={projectID} userID={userID} onQueued={() => { setCursor(""); setReload((value) => value + 1); }} />}</details>}
    <div className="memory-toolbar"><h3>生成任务</h3><button className="icon-button" title="刷新生成任务" aria-label="刷新生成任务" disabled={loading || busy} onClick={() => { setCursor(""); setReload((value) => value + 1); }}><RefreshCw size={16} /></button></div>
    {loading && <p role="status">正在读取生成任务</p>}
    {failure && <p role="alert">{failure}</p>}
    {!loading && !failure && !items.length && <p>暂无生成任务</p>}
    <ul>{items.map((item) => <li key={item.generation_id}><div><strong>{phases[item.phase]} · {item.status === "running" && item.error_code === "MEMORY_GENERATION_PAUSE_REQUESTED" ? "正在暂停" : statuses[item.status]}</strong><small>{item.model_id || "尚未分配模型"} · 尝试 {item.attempt}</small><time dateTime={item.created_at}>{new Date(item.created_at).toLocaleString()}</time></div>{item.error_code && <code>{item.error_code}</code>}
      {editable && ["queued", "running", "paused"].includes(item.status) && <div className="memory-generation-controls">
        {item.status === "paused" ? <><button className="icon-button" aria-label="恢复生成任务" title="恢复生成任务" disabled={loading || busy || unknown || item.resume_available !== true} onClick={() => void control(item, "resume")}><Play size={16} /></button>{item.resume_available !== true && <small>{item.resume_available === false ? "缺少恢复检查点" : "恢复条件未确认"}</small>}</> : <button className="icon-button" aria-label="暂停生成任务" title="暂停生成任务" disabled={loading || busy || unknown || item.error_code === "MEMORY_GENERATION_PAUSE_REQUESTED"} onClick={() => void control(item, "pause")}><Pause size={16} /></button>}
        <button className="icon-button" aria-label="取消生成任务" title="取消生成任务" disabled={loading || busy || unknown} onClick={() => void control(item, "cancel")}><X size={16} /></button>
      </div>}</li>)}</ul>
    {next && <button disabled={loading || busy || unknown} onClick={() => { setCursor(next); setReload((value) => value + 1); }}><ChevronDown size={15} />加载更多</button>}
  </section>;
}
