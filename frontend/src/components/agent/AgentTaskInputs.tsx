import { useEffect, useRef, useState } from "react";
import { Check, LoaderCircle, MessageSquarePlus, Paperclip, Pencil, Undo2, X } from "lucide-react";
import type { AgentTask, AgentTaskInput, AgentTurn, AttachmentRef, InputChangeReceipt, InputChangeScope } from "../../types";
import { createUUID } from "../../uuid";
import { InputChangeEditor } from "./InputChangeEditor";
import { InputAttachments } from "./InputAttachments";

export function AgentTaskInputs({ task, onAppend }: { task: AgentTask; onAppend?: (task: AgentTask, content: string, idempotencyKey: string, attachments?: AttachmentRef[]) => Promise<void> }) {
  return <ExecutionInputs executionID={task.agent_task_id} changeScope={{ projectID: task.project_id, mode: "background_task" }} active={!task.cancel_requested && ["queued", "running", "waiting_approval", "pausing", "paused"].includes(task.status)} paused={task.status === "paused"} inputs={task.additional_inputs ?? []} label="追加后台任务要求" inputLabel="后台任务追加要求" onAppend={onAppend ? (content, key, refs) => refs?.length ? onAppend(task, content, key, refs) : onAppend(task, content, key) : undefined} />;
}

export function AgentTurnInputs({ turn, onAppend }: { turn: AgentTurn; onAppend?: (turn: AgentTurn, content: string, idempotencyKey: string, attachments?: AttachmentRef[]) => Promise<void> }) {
  return <ExecutionInputs executionID={turn.agent_turn_id} changeScope={{ projectID: turn.project_id, mode: "conversation" }} active={["accepted", "running", "waiting_approval", "pausing", "paused"].includes(turn.status)} paused={turn.status === "paused"} inputs={turn.additional_inputs ?? []} label="追加本轮要求" inputLabel="本轮追加要求" caption={turn.request.content} onAppend={onAppend ? (content, key, refs) => refs?.length ? onAppend(turn, content, key, refs) : onAppend(turn, content, key) : undefined} />;
}

export function ExecutionInputs({ executionID, active, paused, inputs, label, inputLabel, caption, onAppend, onDraftChange, changeScope, onChanged }: {
  executionID: string; active: boolean; paused: boolean; inputs: Pick<AgentTaskInput, "input_id" | "content" | "status" | "can_modify" | "attachments">[];
  label: string; inputLabel: string; caption?: string; onAppend?: (content: string, idempotencyKey: string, attachments?: AttachmentRef[]) => Promise<void>; onDraftChange?: (dirty: boolean) => void;
  changeScope?: InputChangeScope; onChanged?: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState("");
  const [attachments, setAttachments] = useState<AttachmentRef[]>([]);
  const [materialBusy, setMaterialBusy] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const [change, setChange] = useState<{ inputID: string; action: "withdraw" | "revise" } | null>(null);
  const [receipts, setReceipts] = useState<Partial<Record<string, InputChangeReceipt>>>({});
  useEffect(() => { onDraftChange?.(Boolean(draft) || Boolean(change) || attachments.length > 0 || materialBusy); }, [draft, change, attachments, materialBusy, onDraftChange]);
  const pending = useRef<{ executionID: string; content: string; attachments: string; key: string } | null>(null);
  const save = async () => {
    if (!onAppend || busy || materialBusy || change || !active || (!draft.trim() && !attachments.length)) return;
    const attachmentKey = JSON.stringify(attachments.map(({ asset_id, asset_snapshot_id }) => ({ asset_id, asset_snapshot_id })));
    if (pending.current?.executionID !== executionID || pending.current.content !== draft || pending.current.attachments !== attachmentKey) {
      pending.current = { executionID, content: draft, attachments: attachmentKey, key: createUUID() };
    }
    setBusy(true); setFailure("");
    try { if (attachments.length) await onAppend(draft, pending.current.key, attachments); else await onAppend(draft, pending.current.key); pending.current = null; setDraft(""); setAttachments([]); setOpen(false); }
    catch (error) { setFailure(error instanceof Error ? error.message : "追加要求失败，草稿已保留。"); }
    finally { setBusy(false); }
  };
  if (!inputs.length && !draft && !attachments.length && (!active || !onAppend)) return null;
  const openLabel = active && onAppend ? label : "查看未提交草稿";
  return <div className={`agent-task-inputs${caption !== undefined ? " agent-turn-inputs" : ""}`}>
    {caption !== undefined && (inputs.length > 0 || open || draft) && <p className="task-input-context" title={caption}>{caption}</p>}
    {inputs.length > 0 && <details><summary>追加要求 · {inputs.length}</summary><ol>{inputs.map((input) => {
      const status = receipts[input.input_id]?.status ?? input.status;
      const allowed = Boolean(input.can_modify && status === "received");
      return <li key={input.input_id}><small>{status === "withdrawn" ? "已撤回，未送入模型" : status === "superseded" ? "已被新修订替代" : status === "included" ? "已送入模型" : active ? "已接收，待送入模型" : "未确认送入模型"}</small><p>{input.content}</p>
        {!!input.attachments?.length && <ul className="input-attachment-list" aria-label="已接收追加材料">{input.attachments.map((item) => <li key={item.asset_id}><Paperclip size={13} /><span title={`${item.name} · ${item.asset_snapshot_id}`}>{item.name}</span></li>)}</ul>}
        {allowed && changeScope && !change && <div className="input-change-actions">{active && <button type="button" className="icon-button ghost" title="修订追加要求" aria-label="修订追加要求" disabled={busy} onClick={() => { setChange({ inputID: input.input_id, action: "revise" }); onDraftChange?.(true); }}><Pencil size={14} /></button>}<button type="button" className="icon-button ghost" title="撤回追加要求" aria-label="撤回追加要求" disabled={busy} onClick={() => { setChange({ inputID: input.input_id, action: "withdraw" }); onDraftChange?.(true); }}><Undo2 size={14} /></button></div>}
        {change?.inputID === input.input_id && changeScope && <InputChangeEditor key={`${input.input_id}:${change.action}`} scope={changeScope} executionID={executionID} input={input} action={change.action} allowed={allowed && (change.action === "withdraw" || active)} onClose={() => { setChange(null); onDraftChange?.(Boolean(draft)); }} onSaved={(receipt) => { setReceipts((current) => ({ ...current, [receipt.input_id]: receipt })); setChange(null); onDraftChange?.(Boolean(draft)); onChanged?.(); }} />}
      </li>;
    })}</ol></details>}
    {paused && inputs.some((input) => input.status === "received") && <p className="task-input-boundary" role="note">继续执行可能先处理已批准的工具或恢复原模型请求，再将追加要求送入模型；实际收件以回执为准。未批准的工具不会因追加要求而获得授权。</p>}
    {!open && ((active && onAppend) || draft || attachments.length > 0) && <button type="button" className="icon-button" title={openLabel} aria-label={openLabel} onClick={() => setOpen(true)}><MessageSquarePlus size={16} /></button>}
    {open && <div className="queued-message-editor">
      <textarea aria-label={inputLabel} autoFocus value={draft} maxLength={8192} disabled={busy} onChange={(event) => { setDraft(event.target.value); onDraftChange?.(Boolean(event.target.value) || Boolean(change)); }} />
      {changeScope && <InputAttachments projectID={changeScope.projectID} value={attachments} disabled={busy || !active || !onAppend || Boolean(change)} onChange={setAttachments} onBusyChange={setMaterialBusy} />}
      <p className="task-input-boundary">接收不代表已送入模型；追加要求不会撤销已有授权，也不回滚已执行的操作。</p>
      {!active && <span className="form-error" role="alert">任务已结束或正在取消，未提交的草稿仍保留在此处。</span>}
      {active && !onAppend && <span className="form-error" role="alert">当前已无追加权限，未提交的草稿仍保留在此处。</span>}
      {failure && <span className="form-error" role="alert">{failure}</span>}
      <div className="queued-message-actions">
        <button type="button" className="icon-button" title="收起追加输入" aria-label="收起追加输入" disabled={busy || materialBusy} onClick={() => setOpen(false)}><X size={16} /></button>
        <button type="button" className="icon-button" title="提交追加要求" aria-label="提交追加要求" disabled={busy || materialBusy || Boolean(change) || !active || !onAppend || (!draft.trim() && !attachments.length)} onClick={() => void save()}>{busy ? <LoaderCircle size={16} /> : <Check size={16} />}</button>
      </div>
    </div>}
  </div>;
}
