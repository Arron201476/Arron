import { useEffect, useRef, useState } from "react";
import { Download, FileText, Paperclip, Search, X } from "lucide-react";
import { api } from "../../api";
import type { Asset, ComposerRegistryEntry } from "../../types";
import { assetAvailable, materialUnavailableReason } from "./projectMaterials";
import "./projectMaterials.css";

export function AssetFileActions({ asset, onUse, disabledReason = "" }: { asset: Asset; onUse?: (asset: Asset) => void; disabledReason?: string }) {
  const reason = disabledReason || materialUnavailableReason(asset);
  return <span className="asset-file-actions">
    {assetAvailable(asset) ? <a className="icon-button ghost" href={api.assetDownloadURL(asset.asset_id)} download={asset.original_filename} target="_blank" rel="noopener noreferrer" aria-label={`下载 ${asset.original_filename}`} title="下载原文件"><Download size={15} /></a> : <button type="button" className="icon-button ghost" disabled aria-label={`下载 ${asset.original_filename}`} title="文件已删除或不可用"><Download size={15} /></button>}
    {onUse && <button type="button" className="icon-button ghost" aria-label={`使用 ${asset.original_filename} 作为材料`} title={reason || "作为材料"} disabled={Boolean(reason)} onClick={() => onUse(asset)}><Paperclip size={15} /></button>}
  </span>;
}

export function ProjectMaterialPicker({ projectID, assets, capability, onClose, onConfirm }: { projectID: string; assets: Asset[]; capability: ComposerRegistryEntry | null; onClose: () => void; onConfirm: (assets: Asset[]) => void }) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const scoped = assets.filter((asset) => asset.project_id === projectID);
  const visible = scoped.filter((asset) => `${asset.display_name} ${asset.original_filename}`.toLowerCase().includes(query.trim().toLowerCase()));
  const chosen = scoped.filter((asset) => selected.includes(asset.asset_id) && !materialUnavailableReason(asset, capability));
  useEffect(() => { dialogRef.current?.showModal(); return () => dialogRef.current?.close(); }, []);
  return <dialog ref={dialogRef} className="project-material-dialog" aria-labelledby="project-material-title" onClose={() => { if (dialogRef.current && !dialogRef.current.open) onClose(); }}>
    <header><h2 id="project-material-title">项目材料</h2><button type="button" className="icon-button ghost" aria-label="关闭项目材料" title="关闭" onClick={onClose}><X size={18} /></button></header>
    <label className="project-material-search"><Search size={15} /><input value={query} onChange={(event) => setQuery(event.target.value)} aria-label="搜索项目材料" placeholder="搜索材料" /></label>
    <div className="project-material-list">
      {visible.map((asset) => { const reason = materialUnavailableReason(asset, capability); return <div className="project-material-row" key={asset.asset_id}>
        <label><input type="checkbox" checked={selected.includes(asset.asset_id)} disabled={Boolean(reason)} onChange={(event) => setSelected((current) => event.target.checked ? [...current, asset.asset_id] : current.filter((id) => id !== asset.asset_id))} /><FileText size={15} /><span><strong>{asset.display_name || asset.original_filename}</strong><small>{reason || (["hosted_tool", "mcp_tool"].includes(asset.source_type ?? "") ? "工具产出" : "已上传")}{asset.size_bytes !== undefined ? ` · ${asset.size_bytes.toLocaleString("zh-CN")} B` : ""}</small></span></label>
        <AssetFileActions asset={asset} />
      </div>; })}
      {!visible.length && <p>{query ? "没有匹配的材料" : "暂无项目材料"}</p>}
    </div>
    <footer><button type="button" className="primary-button" disabled={chosen.length === 0} onClick={() => onConfirm(chosen)}><Paperclip size={15} />使用所选材料{chosen.length ? `（${chosen.length}）` : ""}</button></footer>
  </dialog>;
}
