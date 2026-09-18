import { useEffect, useRef, useState } from "react";
import { RefreshCw, Save } from "lucide-react";
import { api } from "../../api";
import { createUUID } from "../../uuid";
import type { AgentToolOutcomeReview as Review, AgentToolOutcomeCommand } from "../../types";
import "./agentToolOutcomes.css";

type Draft = { key: string; outcome: "" | "applied" | "not_applied"; evidence: string; checked: boolean };

export function AgentToolOutcomeReview({ callID, projectID }: { callID: string; projectID: string }) {
  const [open, setOpen] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [loaded, setLoaded] = useState<{ key: string; view?: Review; error?: string } | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState<string | null>(null);
  const [saveError, setSaveError] = useState<{ key: string; message: string } | null>(null);
  const pending = useRef<{ body: string; requestID: string } | null>(null);
  const submission = useRef<AbortController | null>(null);
  const key = JSON.stringify([callID, projectID, refresh]);
  const current = loaded?.key === key ? loaded : null;
  const view = current?.view;
  const binding = JSON.stringify([key, view?.subject_snapshot_hash]);
  const activeDraft: Draft = draft?.key === binding ? draft : { key: binding, outcome: "", evidence: "", checked: false };
  const busy = saving === binding;

  function validate(result: Review) {
    if (result.agent_tool_call_id !== callID || result.project_id !== projectID) throw new Error("核对记录与当前操作不一致。");
    return result;
  }
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    setLoaded(null);
    void api.getAgentToolOutcomeReview(callID, controller.signal).then((result) => {
      if (!controller.signal.aborted) setLoaded({ key, view: validate(result) });
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setLoaded({ key, error: error instanceof Error ? error.message : "核对记录读取失败。" });
    });
    return () => { controller.abort(); submission.current?.abort(); submission.current = null; };
  }, [callID, projectID, key, open]);

  function edit(values: Partial<Draft>) { setDraft({ ...activeDraft, ...values }); setSaveError(null); }
  async function submit() {
    if (!view?.can_resolve || submission.current || !activeDraft.outcome || !activeDraft.checked || !activeDraft.evidence.trim()) return;
    if (new TextEncoder().encode(activeDraft.evidence).length > 4096) { setSaveError({ key: binding, message: "核对依据超过 4096 字节。" }); return; }
    const content = { subject_snapshot_hash: view.subject_snapshot_hash, outcome: activeDraft.outcome, evidence: activeDraft.evidence };
    const body = JSON.stringify([callID, projectID, content]);
    if (pending.current?.body !== body) pending.current = { body, requestID: createUUID() };
    const command: AgentToolOutcomeCommand = { ...content, request_id: pending.current.requestID };
    const controller = new AbortController();
    submission.current = controller;
    setSaving(binding); setSaveError(null);
    try {
      const result = await api.resolveAgentToolOutcome(callID, command, controller.signal);
      if (controller.signal.aborted) return;
      validate(result);
      const identityFields = ["sdk_tool_call_id", "tool_id", "arguments_hash", "configuration_hash", "user_id", "execution_mode", "execution_id"] as const;
      if (result.subject_snapshot_hash !== command.subject_snapshot_hash || result.resolution?.request_id !== command.request_id
        || result.resolution.outcome !== command.outcome || result.resolution.evidence !== command.evidence
        || result.resolution.actor_user_id !== view.user_id || result.can_resolve
        || identityFields.some((field) => result[field] !== view[field])) throw new Error("核对回执与本次提交不一致。");
      setLoaded({ key, view: result }); setDraft(null);
    } catch (error: unknown) {
      if (!controller.signal.aborted) setSaveError({ key: binding, message: error instanceof Error ? error.message : "核对记录保存失败。" });
    } finally {
      if (submission.current === controller) submission.current = null;
      setSaving((value) => value === binding ? null : value);
    }
  }
  return <details className="agent-tool-outcome" onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary>外部操作结果核对</summary>
    {open && <div aria-live="polite">
      <div className="agent-outcome-heading"><span>原操作：{callID}</span><button type="button" className="icon-button" title="刷新核对记录" aria-label="刷新核对记录" disabled={busy} onClick={() => setRefresh((value) => value + 1)}><RefreshCw size={15} /></button></div>
      {!current && <p role="status">正在读取核对记录</p>}
      {current?.error && <p className="form-error" role="alert">{current.error}</p>}
      {view?.resolution && <><strong>{view.resolution.outcome === "applied" ? "用户确认：操作已发生" : "用户确认：操作未发生"}</strong><pre>{view.resolution.evidence}</pre><small>{new Date(view.resolution.created_at).toLocaleString("zh-CN")}</small></>}
      {view && !view.resolution && <>
        <p className="form-error">外部写入结果未确认。原失败记录保留，不能据此认定操作未发生。</p>
        {view.can_resolve ? <form onSubmit={(event) => { event.preventDefault(); void submit(); }}>
          <fieldset disabled={busy}><legend>实际核对结果</legend>
            <label><input type="radio" name={`${callID}-outcome`} checked={activeDraft.outcome === "applied"} onChange={() => edit({ outcome: "applied" })} />操作已发生</label>
            <label><input type="radio" name={`${callID}-outcome`} checked={activeDraft.outcome === "not_applied"} onChange={() => edit({ outcome: "not_applied" })} />操作未发生</label>
          </fieldset>
          <label>核对依据<textarea value={activeDraft.evidence} disabled={busy} maxLength={4096} required rows={3} onChange={(event) => edit({ evidence: event.target.value })} /></label>
          <label className="agent-outcome-ack"><input type="checkbox" checked={activeDraft.checked} disabled={busy} onChange={(event) => edit({ checked: event.target.checked })} />已核对外部服务中的原操作</label>
          {saveError?.key === binding && <p className="form-error" role="alert">{saveError.message}</p>}
          <button type="submit" className="secondary-button" disabled={busy || !activeDraft.outcome || !activeDraft.checked || !activeDraft.evidence.trim()}><Save size={15} />{busy ? "正在保存" : "保存核对结果"}</button>
        </form> : <p>等待执行暂停或结束后，由原发起人核对。</p>}
      </>}
    </div>}
  </details>;
}
