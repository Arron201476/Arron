import { useEffect, useState } from "react";
import { LoaderCircle, MessageSquarePlus, RefreshCw, X } from "lucide-react";
import { api } from "../../api";
import type { ExecutionInputsView, RunSnapshot } from "../../types";
import { taskProgressLabel } from "../../taskPresentation";
import { ExecutionInputs } from "./AgentTaskInputs";
import { ExecutionInputHistory } from "./ExecutionInputHistory";

export function StatefulRunInputs({ projectID, snapshot }: { projectID: string; snapshot: RunSnapshot }) {
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState("");
  const [selectedLabel, setSelectedLabel] = useState("");
  const [dirty, setDirty] = useState(false);
  const [view, setView] = useState<ExecutionInputsView | null>(null);
  const [failure, setFailure] = useState("");
  const [loading, setLoading] = useState(false);
  const [revision, setRevision] = useState(0);
  const targets = (snapshot.task_items ?? []).filter((task) => task.current_attempt_id);
  const attemptID = selected || targets.find((task) => ["running", "paused", "waiting_approval", "repair_pending"].includes(task.status))?.current_attempt_id || targets[0]?.current_attempt_id || "";
  useEffect(() => {
    if (!open || !attemptID) return;
    const controller = new AbortController();
    setLoading(true); setFailure("");
    void api.getExecutionInputs(projectID, attemptID, controller.signal).then((result) => {
      if (!controller.signal.aborted) { setView(result); setSelected(attemptID); }
    }).catch((error: unknown) => { if (!controller.signal.aborted) setFailure(error instanceof Error ? error.message : "读取追加要求失败。"); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [projectID, attemptID, open, snapshot, revision]);
  const current = view?.attempt_id === attemptID ? view : null;
  const active = current ? ["running", "paused", "waiting_approval", "repair_pending"].includes(current.status) : false;
  return <section className="stateful-run-inputs" aria-label="状态化任务追加要求">
    <div className="stateful-input-controls">
      {attemptID && <button type="button" className="icon-button ghost" aria-label={open ? "收起任务追加要求" : "任务追加要求"} title={open ? "收起任务追加要求" : "任务追加要求"} onClick={() => setOpen(!open)}>{open ? <X size={16} /> : <MessageSquarePlus size={16} />}</button>}
      {open && attemptID && <><label>当前处理项<select aria-label="追加目标任务" value={attemptID} disabled={dirty} onChange={(event) => { setSelected(event.target.value); setSelectedLabel(""); setView(null); }}>
        {selected && !targets.some((task) => task.current_attempt_id === selected) && <option value={selected}>{selectedLabel || "先前选择的执行"}</option>}
        {targets.map((task) => <option key={task.current_attempt_id} value={task.current_attempt_id!}>{taskProgressLabel(task)}</option>)}
      </select></label><button type="button" className="icon-button ghost" title="刷新收件状态" aria-label="刷新收件状态" disabled={loading} onClick={() => setRevision((value) => value + 1)}>{loading ? <LoaderCircle className="task-spinner" size={15} /> : <RefreshCw size={15} />}</button></>}
    </div>
    <ExecutionInputHistory key={`${projectID}:${snapshot.run.run_id}`} projectID={projectID} runID={snapshot.run.run_id} disabled={dirty} onSelect={(item) => { setSelected(item.attempt_id); setSelectedLabel(`${item.item_key} · 第 ${item.attempt_no} 次尝试`); setView(null); setOpen(true); }} />
    {open && failure && <p className="form-error" role="alert">{failure}</p>}
    <div hidden={!open}>
      {current && <ExecutionInputs key={current.attempt_id} executionID={current.attempt_id} active={active} paused={snapshot.run.status === "paused" || current.status === "waiting_approval"} inputs={current.inputs}
        label="追加当前处理项要求" inputLabel="当前处理项追加要求" onDraftChange={setDirty} changeScope={{ projectID, mode: "stateful_workflow" }} onChanged={() => setRevision((value) => value + 1)}
        onAppend={current.can_append && !loading && !failure ? async (content, key, refs) => {
          const input = refs?.length ? await api.appendExecutionInput(projectID, current.attempt_id, content, key, refs) : await api.appendExecutionInput(projectID, current.attempt_id, content, key);
          setView((latest) => latest?.attempt_id === current.attempt_id ? { ...latest, inputs: [...latest.inputs.filter((item) => item.input_id !== input.input_id), input].sort((a, b) => a.sequence - b.sequence) } : latest);
          setRevision((value) => value + 1);
        } : undefined} />}
      {current && !current.can_append && !current.inputs.length && <p className="task-input-boundary">当前执行不可追加。</p>}
    </div>
  </section>;
}
