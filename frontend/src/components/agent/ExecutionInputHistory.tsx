import { useEffect, useRef, useState } from "react";
import { ChevronDown, History, LoaderCircle, RefreshCw } from "lucide-react";
import { api } from "../../api";
import type { ExecutionInputAttempt } from "../../types";

export function ExecutionInputHistory({ projectID, runID, disabled, onSelect }: {
  projectID: string; runID: string; disabled: boolean; onSelect: (attempt: ExecutionInputAttempt) => void;
}) {
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<ExecutionInputAttempt[]>([]);
  const [next, setNext] = useState("");
  const [loading, setLoading] = useState(false);
  const [failure, setFailure] = useState("");
  const controller = useRef<AbortController | null>(null);
  useEffect(() => () => controller.current?.abort(), [projectID, runID]);
  const load = async (cursor = "") => {
    controller.current?.abort();
    const request = new AbortController(); controller.current = request;
    setLoading(true); setFailure("");
    try {
      const page = await api.listExecutionInputAttempts(projectID, runID, cursor, request.signal);
      if (request.signal.aborted) return;
      setItems((previous) => [...new Map([...(cursor ? previous : []), ...page.items].map((item) => [item.attempt_id, item])).values()]);
      setNext(page.next_cursor);
    } catch (error) { if (!request.signal.aborted) setFailure(error instanceof Error ? error.message : "读取历史追加记录失败。"); }
    finally { if (!request.signal.aborted) setLoading(false); }
  };
  return <div className="execution-input-history">
    <button type="button" className="icon-button ghost" title="历史追加记录" aria-label="历史追加记录" aria-expanded={open} onClick={() => { setOpen(!open); if (!open) void load(); }}><History size={16} /></button>
    {open && <div className="execution-input-history-list" role="region" aria-label="历史追加记录列表">
      <div className="execution-input-history-heading"><strong>历史追加记录</strong><button type="button" className="icon-button ghost" title="刷新历史追加记录" aria-label="刷新历史追加记录" disabled={loading} onClick={() => void load()}>{loading ? <LoaderCircle size={15} /> : <RefreshCw size={15} />}</button></div>
      {failure && <p className="form-error" role="alert">{failure}</p>}
      {!loading && !failure && items.length === 0 && <p>尚无追加记录</p>}
      <ul>{items.map((item) => <li key={item.attempt_id}><button type="button" disabled={disabled} onClick={() => { onSelect(item); setOpen(false); }}>
        <span>{item.item_key} · 第 {item.attempt_no} 次尝试{item.current ? " · 当前" : ""}</span>
        <small>{item.input_count} 条追加 · {item.included_count} 条已送入模型</small>
      </button></li>)}</ul>
      {next && <button type="button" className="icon-button ghost" title="更多历史追加记录" aria-label="更多历史追加记录" disabled={loading} onClick={() => void load(next)}><ChevronDown size={16} /></button>}
    </div>}
  </div>;
}
