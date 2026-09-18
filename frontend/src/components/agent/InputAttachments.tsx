import { useEffect, useRef, useState } from "react";
import { FolderOpen, LoaderCircle, Paperclip, X } from "lucide-react";
import { api } from "../../api";
import type { Asset, AttachmentRef } from "../../types";
import { ProjectMaterialPicker } from "../files/ProjectMaterialPicker";

export function InputAttachments({ projectID, value, disabled, onChange, onBusyChange }: {
  projectID: string; value: AttachmentRef[]; disabled: boolean;
  onChange: (value: AttachmentRef[]) => void; onBusyChange: (busy: boolean) => void;
}) {
  const [assets, setAssets] = useState<Asset[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const fileInput = useRef<HTMLInputElement>(null);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => { onBusyChange(busy || assets !== null); }, [busy, assets, onBusyChange]);
  const setWorking = (working: boolean) => { if (mounted.current) setBusy(working); };
  const merge = (selected: Asset[], current = value) => {
    if (selected.some((asset) => asset.project_id !== projectID || asset.status !== "available" || !asset.current_snapshot_id || !asset.size_bytes || asset.size_bytes > 5 * 1024 * 1024)) {
      throw new Error("追加材料须为当前作品的可用文件，单文件不能超过 5 MiB。");
    }
    const merged = new Map(current.map((item) => [item.asset_id, item]));
    selected.forEach((asset) => merged.set(asset.asset_id, { asset_id: asset.asset_id, asset_snapshot_id: asset.current_snapshot_id!, display_name: asset.original_filename, kind: asset.kind }));
    if (merged.size > 4) throw new Error("每条追加要求最多附带 4 份材料；已上传的文件仍保留在项目材料中。");
    return [...merged.values()];
  };
  const choose = async () => {
    if (busy || disabled) return;
    setWorking(true); setFailure("");
    try { const result = await api.listAssets(projectID); if (mounted.current) setAssets(result); }
    catch (error) { if (mounted.current) setFailure(error instanceof Error ? error.message : "读取项目材料失败。"); }
    finally { setWorking(false); }
  };
  const upload = async (files: File[]) => {
    if (busy || disabled || !files.length) return;
    if (files.length + value.length > 4 || files.some((file) => file.size <= 0 || file.size > 5 * 1024 * 1024)) {
      setFailure("每条追加要求最多 4 份材料，单文件须在 1 B 至 5 MiB 之间。"); return;
    }
    setWorking(true); setFailure("");
    let selected = value;
    try {
      for (const file of files) {
        const refs = await api.uploadFiles(projectID, [file]);
        const current = await api.listAssets(projectID);
        const uploaded = current.filter((asset) => refs.some((ref) => ref.asset_id === asset.asset_id && ref.asset_snapshot_id === asset.current_snapshot_id));
        if (!uploaded.length) throw new Error("上传已返回，但未找到对应的材料快照；请在项目材料中核对。");
        selected = merge(uploaded, selected);
        if (mounted.current) onChange(selected);
      }
    } catch (error) { if (mounted.current) setFailure(error instanceof Error ? error.message : "上传失败，已完成的材料仍保留在作品中。"); }
    finally { setWorking(false); if (fileInput.current) fileInput.current.value = ""; }
  };
  return <div className="input-attachments">
    <div className="input-attachment-actions">
      <input className="visually-hidden" ref={fileInput} type="file" multiple aria-label="上传追加材料" disabled={disabled || busy} onChange={(event) => void upload(Array.from(event.target.files ?? []))} />
      <button type="button" className="icon-button ghost" title="上传追加材料" aria-label="上传追加材料" disabled={disabled || busy} onClick={() => fileInput.current?.click()}>{busy ? <LoaderCircle size={16} /> : <Paperclip size={16} />}</button>
      <button type="button" className="icon-button ghost" title="选择追加项目材料" aria-label="选择追加项目材料" disabled={disabled || busy} onClick={() => void choose()}><FolderOpen size={16} /></button>
    </div>
    {value.length > 0 && <ul className="input-attachment-list" aria-label="待追加材料">{value.map((item) => <li key={item.asset_id}><Paperclip size={13} /><span title={item.display_name}>{item.display_name}</span><button type="button" className="icon-button ghost" title={`移除追加材料 ${item.display_name}`} aria-label={`移除追加材料 ${item.display_name}`} disabled={disabled || busy} onClick={() => onChange(value.filter((other) => other.asset_id !== item.asset_id))}><X size={13} /></button></li>)}</ul>}
    {failure && <p className="form-error" role="alert">{failure}</p>}
    {assets && <ProjectMaterialPicker projectID={projectID} assets={assets} capability={null} onClose={() => setAssets(null)} onConfirm={(selected) => {
      if (disabled) { setAssets(null); return; }
      try { onChange(merge(selected)); setFailure(""); setAssets(null); }
      catch (error) { setFailure(error instanceof Error ? error.message : "选择追加材料失败。"); setAssets(null); }
    }} />}
  </div>;
}
