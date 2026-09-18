import { useEffect, useRef, useState } from "react";
import { Save, RefreshCw } from "lucide-react";
import { api, ApiError } from "../../api";
import { createUUID } from "../../uuid";

export interface MemoryPreferencesValue { project_id: string; user_id: string; archive_enabled: boolean; generate_enabled: boolean; revision: number; request_id?: string }
export interface MemoryPreferencesUpdate { archive_enabled: boolean; generate_enabled: boolean; expected_revision: number; request_id: string }
function validate(value: MemoryPreferencesValue, projectID: string, userID: string) {
  if (!value || value.project_id !== projectID || value.user_id !== userID || !Number.isSafeInteger(value.revision) || value.revision < 0 ||
      typeof value.archive_enabled !== "boolean" || typeof value.generate_enabled !== "boolean" || value.generate_enabled && !value.archive_enabled) throw new Error("记忆授权回执与当前用户或作品不一致。");
}
export function MemoryPreferences({ projectID, userID, editable }: { projectID: string; userID: string; editable: boolean }) {
  const [value, setValue] = useState<MemoryPreferencesValue | null>(null);
  const [archive, setArchive] = useState(false);
  const [generate, setGenerate] = useState(false);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [unknown, setUnknown] = useState(false);
  const [failure, setFailure] = useState("");
  const [reload, setReload] = useState(0);
  const pending = useRef<MemoryPreferencesUpdate | null>(null);
  const submitting = useRef(false);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setValue(null); setFailure("");
    api.getMemoryPreferences(projectID, controller.signal).then((result) => {
      if (controller.signal.aborted) return;
      validate(result, projectID, userID); setValue(result); setArchive(result.archive_enabled); setGenerate(result.generate_enabled);
    }).catch((error) => { if (!controller.signal.aborted) setFailure(error instanceof Error ? error.message : "读取授权失败。"); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [projectID, userID, reload]);
  const save = async () => {
    if (!value || !editable || submitting.current || loading) return;
    const command = pending.current ?? { archive_enabled: archive, generate_enabled: generate, expected_revision: value.revision, request_id: createUUID() };
    pending.current = command; submitting.current = true; setBusy(true); setFailure("");
    try {
      const result = await api.updateMemoryPreferences(projectID, command);
      if (!mounted.current) return;
      validate(result, projectID, userID);
      if (result.request_id !== command.request_id || result.revision !== command.expected_revision + 1 || result.archive_enabled !== command.archive_enabled || result.generate_enabled !== command.generate_enabled) throw new Error("记忆授权保存结果未确认。");
      pending.current = null; setUnknown(false); setValue(result); setArchive(result.archive_enabled); setGenerate(result.generate_enabled);
    } catch (error) {
      if (!mounted.current) return;
      const definitive = error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS";
      if (definitive) pending.current = null;
      setUnknown(!definitive); setFailure(error instanceof Error ? error.message : "保存授权失败。");
    } finally { submitting.current = false; if (mounted.current) setBusy(false); }
  };
  const disabled = !editable || !value || loading || busy || unknown;
  return <section className="memory-preferences" aria-label="记忆归档授权">
    {loading && <p role="status">正在读取授权</p>}
    <label><input type="checkbox" checked={archive} disabled={disabled} onChange={(event) => { setArchive(event.target.checked); if (!event.target.checked) setGenerate(false); }} />允许将本作品对话归档为本人记忆来源</label>
    <label><input type="checkbox" checked={generate} disabled={disabled || !archive} onChange={(event) => setGenerate(event.target.checked)} />允许从归档自动生成私有记忆</label>
    <div className="memory-toolbar"><button disabled={!editable || !value || loading || busy || !unknown && archive === value.archive_enabled && generate === value.generate_enabled} onClick={() => void save()}><Save size={15} />{unknown ? "重试原授权请求" : "保存授权"}</button>
      <button className="icon-button" aria-label="刷新授权" title="刷新授权" disabled={loading || busy || unknown} onClick={() => setReload((value) => value + 1)}><RefreshCw size={15} /></button></div>
    {failure && <p role="alert">{failure}</p>}
  </section>;
}
