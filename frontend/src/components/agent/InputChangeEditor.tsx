import { useRef, useState } from "react";
import { Check, LoaderCircle, X } from "lucide-react";
import { api } from "../../api";
import { createUUID } from "../../uuid";
import type { InputChangeReceipt, InputChangeScope } from "../../types";

export function InputChangeEditor({ scope, executionID, input, action, allowed, onClose, onSaved }: {
  scope: InputChangeScope; executionID: string; input: { input_id: string; content: string };
  action: "withdraw" | "revise"; allowed: boolean; onClose: () => void; onSaved: (receipt: InputChangeReceipt) => void;
}) {
  const [content, setContent] = useState(action === "revise" ? input.content : "");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const pending = useRef<{ content: string; key: string } | null>(null);
  const save = async () => {
    if (busy || !allowed || (action === "revise" && !content.trim())) return;
    if (!pending.current || pending.current.content !== content) pending.current = { content, key: createUUID() };
    setBusy(true); setFailure("");
    try {
      const receipt = await api.changeExecutionInput(scope, executionID, input.input_id, action, content, pending.current.key);
      if (receipt.mode !== scope.mode || receipt.execution_id !== executionID || receipt.input_id !== input.input_id || receipt.status !== (action === "withdraw" ? "withdrawn" : "superseded") || (action === "revise" && !receipt.replacement_input_id)) throw new Error("追加变更回执不匹配，保存状态待核对。");
      onSaved(receipt);
    } catch (error) { setFailure(error instanceof Error ? error.message : "追加变更失败，内容已保留。"); }
    finally { setBusy(false); }
  };
  return <div className="queued-message-editor input-change-editor" role="group" aria-label={action === "withdraw" ? "撤回追加要求" : "修订追加要求"}>
    {action === "revise" ? <textarea aria-label="修订内容" autoFocus value={content} maxLength={8192} disabled={busy} onChange={(event) => setContent(event.target.value)} /> : <p>确认撤回这条尚未领取的要求？原记录仍保留，已执行的操作不会回滚。</p>}
    {!allowed && <p className="form-error" role="alert">当前要求已被领取、已变更或无修改权限，未提交内容仍保留。</p>}
    {failure && <p className="form-error" role="alert">{failure}</p>}
    <div className="queued-message-actions">
      <button type="button" className="icon-button" aria-label="取消追加变更" title="取消追加变更" disabled={busy} onClick={onClose}><X size={16} /></button>
      <button type="button" className="icon-button" aria-label={action === "withdraw" ? "确认撤回追加要求" : "提交修订要求"} title={action === "withdraw" ? "确认撤回追加要求" : "提交修订要求"} disabled={busy || !allowed || (action === "revise" && !content.trim())} onClick={() => void save()}>{busy ? <LoaderCircle size={16} /> : <Check size={16} />}</button>
    </div>
  </div>;
}
