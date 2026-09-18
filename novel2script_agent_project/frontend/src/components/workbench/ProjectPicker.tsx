import { Clock3, FilePlus2, FolderOpen, Loader2, Trash2, X } from "lucide-react";
import { useState } from "react";
import type { Project, SourceMode } from "../../api/types";
import { runStatusLabel, sourceModeLabel } from "../../lib/workbenchLabels";
import { Button } from "../ui/Button";

interface ProjectPickerProps {
  activeProjectID?: string;
  busy?: boolean;
  error?: string;
  projects: Project[];
  onClose?: () => void;
  onCreate: (title: string, sourceMode: SourceMode) => Promise<void>;
  onDelete: (project: Project) => Promise<void>;
  onOpen: (project: Project) => Promise<void>;
}

export function ProjectPicker({ activeProjectID, busy, error, projects, onClose, onCreate, onDelete, onOpen }: ProjectPickerProps) {
  const [title, setTitle] = useState("");
  const [sourceMode, setSourceMode] = useState<SourceMode>("auto");
  const [creating, setCreating] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<Project | null>(null);
  const [deleting, setDeleting] = useState(false);
  const ordered = projects.slice().sort((left, right) =>
    String(right.updated_at || "").localeCompare(String(left.updated_at || "")),
  );

  async function create() {
    const value = title.trim();
    if (!value || creating || busy) return;
    setCreating(true);
    try {
      await onCreate(value, sourceMode);
      setTitle("");
      setSourceMode("auto");
    } finally {
      setCreating(false);
    }
  }

  async function confirmDelete() {
    if (!deleteTarget || deleting || busy) return;
    setDeleting(true);
    try {
      await onDelete(deleteTarget);
      setDeleteTarget(null);
    } catch {
      // The parent renders the structured API error without closing this dialog.
    } finally {
      setDeleting(false);
    }
  }

  return (
    <div className="project-picker-backdrop" role="presentation">
      <section aria-labelledby="project-picker-title" aria-modal="true" className="project-picker" role="dialog">
        <header className="project-picker-head">
          <div>
            <p>作品中心</p>
            <h1 id="project-picker-title">选择一个作品工作区</h1>
          </div>
          {onClose ? (
            <button aria-label="关闭作品中心" className="project-picker-close" onClick={onClose} title="关闭" type="button">
              <X size={18} />
            </button>
          ) : null}
        </header>

        <div className="project-create-row">
          <label>
            <span>作品名称</span>
            <input
              autoComplete="off"
              autoFocus={!ordered.length}
              name="project-title"
              onChange={(event) => setTitle(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") void create();
              }}
              placeholder="例如：失眠主角短剧"
              value={title}
            />
          </label>
          <label>
            <span>素材类型</span>
            <select name="project-source-mode" onChange={(event) => setSourceMode(event.target.value as SourceMode)} value={sourceMode}>
              <option value="auto">自动识别</option>
              <option value="novel">小说改编</option>
              <option value="non_novel">灵感 / 梗概</option>
            </select>
          </label>
          <Button disabled={!title.trim() || busy || creating} loading={creating} onClick={() => void create()} variant="primary">
            <FilePlus2 size={16} />
            新建作品
          </Button>
        </div>

        {error ? <p className="project-picker-error">{error}</p> : null}

        <div className="project-list-head">
          <h2>已有作品</h2>
          <span>{ordered.length} 个</span>
        </div>
        <div className="project-list">
          {ordered.length ? ordered.map((project) => {
            const deletionBlocked = project.status === "running";
            return (
              <div className={`project-list-row ${project.project_id === activeProjectID ? "active" : ""}`} key={project.project_id}>
                <button className="project-list-item" disabled={busy} onClick={() => void onOpen(project)} type="button">
                  <span className="project-list-icon"><FolderOpen size={18} /></span>
                  <span className="project-list-copy">
                    <strong>{project.title}</strong>
                    <span>{sourceModeLabel(project.source_mode)} · {runStatusLabel(project.status)}</span>
                  </span>
                  <span className="project-list-time">
                    <Clock3 size={13} />
                    {formatProjectTime(project.updated_at)}
                  </span>
                  {project.project_id === activeProjectID ? <span className="project-current">当前</span> : null}
                </button>
                <button
                  aria-label={`删除作品：${project.title}`}
                  className="project-delete-button"
                  disabled={busy || deletionBlocked}
                  onClick={() => setDeleteTarget(project)}
                  title={deletionBlocked ? "作品正在运行，请先暂停或等待完成" : "删除作品"}
                  type="button"
                >
                  <Trash2 size={16} />
                </button>
              </div>
            );
          }) : (
            <div className="project-list-empty">
              <FolderOpen size={24} />
              <p>还没有作品。填写名称后新建第一个作品工作区。</p>
            </div>
          )}
        </div>
        {busy ? <div className="project-picker-loading"><Loader2 className="spin" size={16} />正在打开作品...</div> : null}
        {deleteTarget ? (
          <div className="project-delete-backdrop" role="presentation">
            <section aria-describedby="project-delete-description" aria-labelledby="project-delete-title" aria-modal="true" className="project-delete-dialog" role="alertdialog">
              <h2 id="project-delete-title">删除“{deleteTarget.title}”?</h2>
              <p id="project-delete-description">聊天记录、上传附件、运行记录和所有产物都会永久删除，此操作无法撤销。</p>
              <div className="project-delete-actions">
                <Button className="project-delete-confirm" disabled={deleting} loading={deleting} onClick={() => void confirmDelete()}>确认删除</Button>
                <Button disabled={deleting} onClick={() => setDeleteTarget(null)} variant="ghost">取消</Button>
              </div>
            </section>
          </div>
        ) : null}
      </section>
    </div>
  );
}

function formatProjectTime(value?: string) {
  if (!value) return "尚未更新";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "尚未更新";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date);
}
