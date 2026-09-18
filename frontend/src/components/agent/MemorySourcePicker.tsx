import { useEffect, useRef, useState } from "react";
import { Plus, RefreshCw, ChevronDown } from "lucide-react";
import { api, ApiError } from "../../api";

export interface MemorySource { activity_key: string; segment_id: string; source_hash: string; generation_id?: string }
export interface MemorySourcePage { project_id: string; user_id: string; items: MemorySource[]; next_cursor?: string }
export interface MemoryQueueReceipt extends MemorySource { generation_id: string; project_id: string; user_id: string; revision: number; status: string }
const key = (item: MemorySource) => JSON.stringify([item.activity_key, item.segment_id]);

export function MemorySourcePicker({ projectID, userID, onQueued }: { projectID: string; userID: string; onQueued: () => void }) {
  const [items, setItems] = useState<MemorySource[]>([]);
  const [selected, setSelected] = useState("");
  const [cursor, setCursor] = useState("");
  const [next, setNext] = useState("");
  const [reload, setReload] = useState(0);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const [unknown, setUnknown] = useState(false);
  const pending = useRef<MemorySource | null>(null);
  const submitting = useRef(false);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setFailure("");
    if (!cursor) { setItems([]); setSelected(""); setNext(""); }
    api.listMemorySources(projectID, cursor, controller.signal).then((page) => {
      if (controller.signal.aborted) return;
      const ids = new Set<string>();
      if (!page || page.project_id !== projectID || page.user_id !== userID || !Array.isArray(page.items) || page.items.length > 50 ||
          page.next_cursor !== undefined && (typeof page.next_cursor !== "string" || page.next_cursor.length > 768 || page.next_cursor === cursor && Boolean(cursor))) throw new Error("来源列表与当前用户或作品不一致。");
      for (const item of page.items) {
        if (!item || typeof item.activity_key !== "string" || !/^(turn|background|execution):[^\s:]{1,256}$/.test(item.activity_key) ||
            typeof item.segment_id !== "string" || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(item.segment_id) ||
            typeof item.source_hash !== "string" || !/^[a-f0-9]{64}$/.test(item.source_hash) || ids.has(key(item)) ||
            item.generation_id !== undefined && typeof item.generation_id !== "string") throw new Error("来源引用回执无效。");
        ids.add(key(item));
      }
      setItems((old) => cursor ? [...old.filter((row) => !ids.has(key(row))), ...page.items] : page.items);
      setNext(page.next_cursor ?? "");
    }).catch((error) => { if (!controller.signal.aborted) setFailure(error instanceof Error ? error.message : "来源读取失败。"); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [projectID, userID, cursor, reload]);
  const enqueue = async () => {
    if (submitting.current || loading) return;
    const source = pending.current ?? items.find((item) => key(item) === selected && !item.generation_id);
    if (!source) return;
    pending.current = source; submitting.current = true; setBusy(true); setFailure("");
    try {
      const value = await api.queueMemoryGeneration(projectID, { activity_key: source.activity_key, segment_id: source.segment_id, source_hash: source.source_hash });
      if (!mounted.current) return;
      if (!value || value.project_id !== projectID || value.user_id !== userID || value.activity_key !== source.activity_key || value.segment_id !== source.segment_id ||
          value.source_hash !== source.source_hash || typeof value.generation_id !== "string" || !value.generation_id || !Number.isSafeInteger(value.revision) || value.revision < 1 ||
          !["queued", "running", "paused", "cancelled", "completed", "failed"].includes(value.status)) throw new Error("生成任务排队回执未确认。");
      pending.current = null; setUnknown(false); setCursor(""); setReload((value) => value + 1); onQueued();
    } catch (error) {
      if (!mounted.current) return;
      const definitive = error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS";
      if (definitive) pending.current = null;
      setUnknown(!definitive); setFailure(error instanceof Error ? error.message : "排队结果未确认。");
    } finally { submitting.current = false; if (mounted.current) setBusy(false); }
  };
  return <div className="memory-source-picker">
    <div className="memory-toolbar"><label>归档来源<select aria-label="记忆生成来源" value={selected} disabled={loading || busy || unknown} onChange={(event) => setSelected(event.target.value)}><option value="">选择来源</option>{items.map((item) => <option key={key(item)} value={key(item)} disabled={Boolean(item.generation_id)}>{item.activity_key} · {item.segment_id}{item.generation_id ? " · 已创建任务" : ""}</option>)}</select></label>
      <button className="icon-button" title="刷新来源" aria-label="刷新来源" disabled={loading || busy || unknown} onClick={() => { setCursor(""); setReload((value) => value + 1); }}><RefreshCw size={16} /></button></div>
    {loading && <p role="status">正在读取来源</p>}{!loading && !failure && !items.length && <p>暂无已归档来源</p>}
    {next && <button disabled={loading || busy || unknown} onClick={() => { setCursor(next); setReload((value) => value + 1); }}><ChevronDown size={15} />更多来源</button>}
    <button disabled={loading || busy || (!unknown && !selected)} onClick={() => void enqueue()}><Plus size={15} />{unknown ? "重试原排队请求" : "生成记忆"}</button>
    {failure && <p role="alert">{failure}</p>}
  </div>;
}
