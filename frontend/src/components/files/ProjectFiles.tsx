import { useEffect, useRef, useState } from "react";
import { BadgeCheck, ChevronLeft, ChevronRight, Download, File, FileText, FolderOpen, RefreshCw, X } from "lucide-react";
import { api, ApiError } from "../../api";
import type { Principal, ProjectFile, ProjectFileContent } from "../../types";
import "./projectFiles.css";
import { SkillDraftPanel } from "./SkillDraftPanel";

export function ProjectFiles({ projectID, revisionKey, onInstalled, principal }: { projectID: string; revisionKey: string; principal?: Principal | null; onInstalled?: () => void | Promise<void> }) {
  const [open, setOpen] = useState(false);
  const [openedProject, setOpenedProject] = useState<string | null>(null);
  return <>
    <button type="button" className="icon-button" aria-label="工作文件" title="工作文件" onClick={() => { setOpenedProject(projectID); setOpen(true); }}><FolderOpen size={17} /></button>
    {openedProject === projectID && <ProjectFileDialog key={projectID} open={open} projectID={projectID} principal={principal} revisionKey={revisionKey} onInstalled={onInstalled} onClose={() => setOpen(false)} />}
  </>;
}

function ProjectFileDialog({ projectID, revisionKey, onClose, onInstalled, open, principal }: { projectID: string; revisionKey: string; open: boolean; principal?: Principal | null; onClose: () => void; onInstalled?: () => void | Promise<void> }) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const [files, setFiles] = useState<ProjectFile[]>([]);
  const [reload, setReload] = useState(0);
  const [loading, setLoading] = useState(true);
  const [failure, setFailure] = useState("");
  const [selected, setSelected] = useState<{ path: string; version: number } | null>(null);
  const [offset, setOffset] = useState(0);
  const [page, setPage] = useState<ProjectFileContent | null>(null);
  const [reading, setReading] = useState(false);
  const [readFailure, setReadFailure] = useState("");
  const [skillRoot, setSkillRoot] = useState<string | null>(null);
  const [skillPending, setSkillPending] = useState(false);
  const selectedFile = files.find((file) => file.path === selected?.path);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (open) dialog?.showModal();
    else dialog?.close();
    return () => dialog?.close();
  }, [open]);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setFailure("");
    api.listProjectFiles(projectID, controller.signal).then((result) => {
      if (!Array.isArray(result) || result.some((file) => file.project_id !== projectID || !file.path || !Number.isSafeInteger(file.version) || file.version < 1) || new Set(result.map((file) => file.path)).size !== result.length) throw new ApiError("PROJECT_FILE_RECEIPT_INVALID", "工作文件列表与当前作品不一致。", 502);
      if (!controller.signal.aborted) setFiles(result);
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setFailure(error instanceof Error ? error.message : "无法读取工作文件。");
    }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [projectID, revisionKey, reload]);

  useEffect(() => {
    setPage(null);
    setReadFailure("");
    if (!selected) return;
    const controller = new AbortController();
    setReading(true);
    api.readProjectFile(projectID, selected.path, selected.version, offset, controller.signal).then((result) => {
      if (result?.file?.project_id !== projectID || result.file.path !== selected.path || result.file.version !== selected.version || result.offset !== offset || !Number.isSafeInteger(result.next_offset) || result.next_offset < offset || result.truncated && result.next_offset <= offset) throw new ApiError("PROJECT_FILE_RECEIPT_INVALID", "文件读取结果与所选作品、路径或版本不一致。", 502);
      if (!controller.signal.aborted) setPage(result);
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setReadFailure(error instanceof Error ? error.message : "无法读取文件内容。");
    }).finally(() => { if (!controller.signal.aborted) setReading(false); });
    return () => controller.abort();
  }, [projectID, selected, offset, reload]);

  const selectFile = (file: ProjectFile) => {
    if (skillPending) return;
    setSkillRoot(null);
    setOffset(0);
    setSelected({ path: file.path, version: file.deleted ? Math.max(1, file.version - 1) : file.version });
  };

  return <dialog ref={dialogRef} className="project-files-dialog" aria-labelledby="project-files-title" onCancel={(event) => { event.preventDefault(); onClose(); }} onClose={() => {
    if (dialogRef.current && !dialogRef.current.open) onClose();
  }}>
    <header className="project-files-head">
      <h2 id="project-files-title">工作文件</h2>
      <button type="button" className="icon-button ghost" aria-label="刷新工作文件" title="刷新工作文件" disabled={loading || skillPending} onClick={() => { if (!skillPending) setReload((value) => value + 1); }}><RefreshCw size={16} /></button>
      <button type="button" className="icon-button ghost" aria-label="关闭工作文件" title="关闭" onClick={onClose}><X size={18} /></button>
    </header>
    <div className="project-files-body">
      <nav className="project-files-list" aria-label="项目文件" aria-busy={loading}>
        {failure && <p className="form-error" role="alert">{failure}</p>}
        {loading && files.length === 0 ? <p role="status">正在读取文件…</p> : !failure && files.length === 0 ? <p>暂无工作文件</p> : null}
        {files.map((file) => <button type="button" key={file.path} className={`project-file-entry${selected?.path === file.path ? " selected" : ""}`} disabled={skillPending} aria-pressed={selected?.path === file.path} onClick={() => selectFile(file)}>
          {file.binary ? <File size={15} /> : <FileText size={15} />}<span><strong>{file.path}</strong><small>{file.deleted ? "已删除" : `${file.size_bytes.toLocaleString("zh-CN")} B`} · v{file.version}</small></span>
        </button>)}
      </nav>
      <section className="project-file-reader" aria-label="文件内容" aria-busy={reading}>
        {skillRoot !== null ? <SkillDraftPanel key={skillRoot} projectID={projectID} root={skillRoot} principal={principal} revisionKey={JSON.stringify([reload, revisionKey])} onBack={() => setSkillRoot(null)} onInstalled={onInstalled} onPendingChange={setSkillPending} /> : selected ? <>
          <div className="project-file-toolbar">
            <strong>{selected.path}</strong>
            <label>版本<select aria-label="文件版本" value={selected.version} onChange={(event) => { setOffset(0); setSelected({ ...selected, version: Number(event.target.value) }); }}>
              {Array.from({ length: selectedFile?.version ?? selected.version }, (_, index) => (selectedFile?.version ?? selected.version) - index).map((version) => <option key={version} value={version}>v{version}</option>)}
            </select></label>
            {page && <a className="icon-button ghost" href={api.projectFileDownloadURL(projectID, page.file.path, page.file.version)} download={page.file.path.split("/").at(-1)} title="下载此版本" aria-label="下载此版本"><Download size={16} /></a>}
            {page && !page.file.binary && selected.path.split("/").at(-1) === "SKILL.md" && !selectedFile?.deleted && selected.version === selectedFile?.version && <button type="button" className="icon-button ghost" title="校验 Skill" aria-label="校验 Skill" onClick={() => setSkillRoot(selected.path === "SKILL.md" ? "" : selected.path.slice(0, -"/SKILL.md".length))}><BadgeCheck size={16} /></button>}
          </div>
          {reading ? <p role="status">正在读取内容…</p> : readFailure ? <p className="form-error" role="alert">{readFailure}</p> : page?.file.binary ? <div className="project-file-binary">
            <File size={28} aria-hidden="true" /><p>二进制文件</p>
            <dl><dt>大小</dt><dd>{page.file.size_bytes.toLocaleString("zh-CN")} B</dd><dt>SHA-256</dt><dd>{page.file.content_hash}</dd></dl>
          </div> : page ? <pre tabIndex={0}>{page.content || "（空文件）"}</pre> : null}
          {!page?.file.binary && <footer className="project-file-pagination">
            <span>{page ? `${page.offset.toLocaleString("zh-CN")}–${page.next_offset.toLocaleString("zh-CN")} 字符` : ""}</span>
            <button type="button" className="icon-button ghost" title="上一页" aria-label="上一页" disabled={reading || offset === 0} onClick={() => setOffset(Math.max(0, offset - 16000))}><ChevronLeft size={16} /></button>
            <button type="button" className="icon-button ghost" title="下一页" aria-label="下一页" disabled={reading || !page?.truncated} onClick={() => page && setOffset(page.next_offset)}><ChevronRight size={16} /></button>
          </footer>}
        </> : <p className="project-files-empty">未选择文件</p>}
      </section>
    </div>
  </dialog>;
}
