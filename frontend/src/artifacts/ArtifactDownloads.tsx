import { useEffect, useRef, useState } from "react";
import { Download, RefreshCw, X } from "lucide-react";
import { api } from "../api";
import type { ArtifactDelivery } from "../types";
import "./artifactDownloads.css";

export function ArtifactDownloads({ artifactID, versionID, disabledReason = "" }: { artifactID: string; versionID: string; disabledReason?: string }) {
  const [open, setOpen] = useState(false);
  useEffect(() => { setOpen(false); }, [artifactID, versionID, disabledReason]);
  return <>
    <button type="button" className="icon-button" aria-label="下载产物" title={disabledReason || "下载产物"} disabled={Boolean(disabledReason)} onClick={() => setOpen(true)}><Download size={16} /></button>
    {open && !disabledReason && <ArtifactDownloadDialog key={`${artifactID}:${versionID}`} artifactID={artifactID} versionID={versionID} onClose={() => setOpen(false)} />}
  </>;
}

function ArtifactDownloadDialog({ artifactID, versionID, onClose }: { artifactID: string; versionID: string; onClose: () => void }) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const downloadRef = useRef<AbortController | null>(null);
  const [delivery, setDelivery] = useState<ArtifactDelivery | null>(null);
  const [failure, setFailure] = useState("");
  const [loading, setLoading] = useState(true);
  const [downloading, setDownloading] = useState(false);
  const [format, setFormat] = useState("");
  const [reload, setReload] = useState(0);

  useEffect(() => {
    dialogRef.current?.showModal();
    return () => { downloadRef.current?.abort(); dialogRef.current?.close(); };
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    setDelivery(null); setLoading(true); setFailure("");
    api.getArtifactDelivery(versionID, controller.signal).then((result) => {
      if (controller.signal.aborted) return;
      if (result.artifact_id !== artifactID || result.artifact_version_id !== versionID) throw new Error("下载回执与当前产物版本不符。");
      if (!Number.isSafeInteger(result.version) || result.version < 1 || typeof result.title !== "string" || typeof result.status !== "string" ||
          !Array.isArray(result.downloads) || !result.downloads.length ||
          result.downloads.some((item) => !item || typeof item.format !== "string" || !item.format.trim() || typeof item.filename !== "string" || !item.filename.trim()) ||
          new Set(result.downloads.map((item) => item.format)).size !== result.downloads.length ||
          !Array.isArray(result.warnings) || result.warnings.some((warning) => typeof warning !== "string")) {
        throw new Error("下载格式回执无效，请重新读取。");
      }
      setDelivery(result);
      setFormat(result.downloads.find((item) => item.format === "txt" || item.format === "csv")?.format ?? result.downloads[0]?.format ?? "");
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setFailure(error instanceof Error ? error.message : "无法读取下载格式。");
    }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [artifactID, versionID, reload]);

  const download = async () => {
    const item = delivery?.downloads.find((item) => item.format === format);
    if (!item || downloadRef.current) return;
    const controller = new AbortController();
    downloadRef.current = controller;
    setDownloading(true); setFailure("");
    try {
      // Use the authenticated API route, never a model-provided URL.
      const blob = await api.downloadArtifactVersion(versionID, item.format, controller.signal);
      if (controller.signal.aborted) return;
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url; link.download = item.filename;
      document.body.append(link); link.click(); link.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (error: unknown) {
      if (!controller.signal.aborted) setFailure(error instanceof Error ? error.message : "下载失败，请重试。");
    } finally {
      if (!controller.signal.aborted) setDownloading(false);
      if (downloadRef.current === controller) downloadRef.current = null;
    }
  };

  return <dialog ref={dialogRef} className="artifact-download-dialog" aria-labelledby="artifact-download-title" onClose={() => { if (dialogRef.current && !dialogRef.current.open) onClose(); }}>
    <header><h2 id="artifact-download-title">下载产物</h2><button type="button" className="icon-button ghost" aria-label="关闭下载" title="关闭" onClick={onClose}><X size={18} /></button></header>
    {loading ? <p role="status">正在读取下载格式…</p> : delivery && <>
      <p className="artifact-download-name">{delivery.title}</p>
      <p>版本 {delivery.version} · {delivery.status === "confirmed" ? "已确认" : delivery.status === "superseded" ? "历史版本" : delivery.status === "draft" || delivery.status === "pending_approval" ? "待确认" : delivery.status === "stale" ? "待重新生成" : delivery.status}</p>
      <label className="artifact-download-format">格式<select aria-label="下载格式" value={format} disabled={downloading} onChange={(event) => setFormat(event.target.value)}>{delivery.downloads.map((item) => <option key={item.format} value={item.format}>{item.format === "docx" ? "DOCX（文本）" : item.format.toUpperCase()}</option>)}</select></label>
      {delivery.warnings.map((warning, index) => <p className="artifact-download-warning" key={index}>{warning}</p>)}
    </>}
    {failure && <p className="form-error" role="alert">{failure}</p>}
    <footer>{!loading && !delivery ? <button type="button" className="secondary-button" onClick={() => setReload((value) => value + 1)}><RefreshCw size={15} />重试</button> : <button type="button" className="primary-button" disabled={loading || downloading || !format} onClick={() => void download()}><Download size={15} />{downloading ? "正在下载…" : "下载"}</button>}</footer>
  </dialog>;
}
