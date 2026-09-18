import { useEffect, useState } from "react";
import { RefreshCw } from "lucide-react";
import { api } from "../../api";
import type { AgentSubtaskResult as SubtaskResult } from "../../types";
import "./agentSubtasks.css";

export function AgentSubtaskResult({ callID, projectID }: { callID: string; projectID: string }) {
  const [open, setOpen] = useState(false);
  const [retry, setRetry] = useState(0);
  const [loaded, setLoaded] = useState<{ key: string; result?: SubtaskResult; error?: string } | null>(null);
  const key = JSON.stringify([callID, projectID, retry]);
  const current = loaded?.key === key ? loaded : null;
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    setLoaded(null);
    void api.getAgentSubtaskResult(callID, controller.signal).then((result) => {
      if (controller.signal.aborted) return;
      if (result.agent_tool_call_id !== callID || result.project_id !== projectID || result.schema_version !== "agent_subtask.v1") throw new Error("子任务结果与当前执行不一致。");
      setLoaded({ key, result });
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setLoaded({ key, error: error instanceof Error ? error.message : "子任务结果读取失败。" });
    });
    return () => controller.abort();
  }, [callID, projectID, key, open]);
  return <details className="agent-subtask-result" onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>子任务结果</summary>
    {open && <div aria-live="polite">
      {!current && <p role="status">正在读取子任务结果</p>}
      {current?.error && <div className="agent-subtask-error"><p className="form-error" role="alert">{current.error}</p><button type="button" className="icon-button" title="重试读取子任务结果" aria-label="重试读取子任务结果" onClick={() => setRetry((value) => value + 1)}><RefreshCw size={15} /></button></div>}
      {current?.result && <><pre>{current.result.text}</pre>{current.result.inspected_artifacts.length > 0 && <dl aria-label="子任务依据版本">{current.result.inspected_artifacts.map((ref) => <div key={ref.artifact_version_id}><dt>{ref.artifact_id}</dt><dd>{ref.artifact_version_id}</dd></div>)}</dl>}</>}
    </div>}
  </details>;
}
