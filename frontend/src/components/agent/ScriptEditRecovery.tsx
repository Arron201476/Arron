import { useEffect, useRef, useState } from "react";
import { LoaderCircle, RefreshCw } from "lucide-react";
import { ApiError } from "../../api";
import type { PendingScriptEdit } from "../../types";

type Props = { pending: PendingScriptEdit; readOnly?: boolean; onComplete: (pending: PendingScriptEdit) => Promise<void> };

export function ScriptEditRecovery(props: Props) {
  const pending = props.pending;
  const identity = JSON.stringify([pending.project_id, pending.run_id, pending.step_run_id, pending.expected_script_version_ids, pending.can_complete]);
  return <ScriptEditRecoveryForm key={identity} {...props} />;
}

function ScriptEditRecoveryForm({ pending, readOnly = false, onComplete }: Props) {
  const submitting = useRef(false);
  const mounted = useRef(true);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const versions = pending.expected_script_version_ids;
  const valid = Array.isArray(versions) && versions.length > 0 && versions.every((id) => typeof id === "string" && id.length > 0) && new Set(versions).size === versions.length;
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const submit = async () => {
    if (readOnly || !pending.can_complete || !valid || submitting.current) return;
    submitting.current = true; setBusy(true); setFailure("");
    try { await onComplete(pending); }
    catch (error) { if (mounted.current) setFailure(error instanceof ApiError ? error.message : "交接更新结果尚未确认，请重试。"); }
    finally { submitting.current = false; if (mounted.current) setBusy(false); }
  };
  return <div className="script-edit-recovery" aria-label="待更新的剧本交接">
    <p role="status">{valid ? `${versions.length} 项剧本已保存，交接待更新。` : "交接版本信息不完整，暂时不能更新。"}</p>
    {pending.disabled_reason && <p className="form-error">{pending.disabled_reason}</p>}
    {failure && <p className="form-error" role="alert">{failure}</p>}
    {!readOnly && <button type="button" className="secondary-button" disabled={busy || !pending.can_complete || !valid} onClick={() => void submit()}>
      {busy ? <LoaderCircle size={13} className="task-spinner" aria-hidden="true" /> : <RefreshCw size={13} aria-hidden="true" />}{busy ? "正在更新交接" : "更新交接"}
    </button>}
  </div>;
}
