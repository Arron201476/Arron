import { Activity, BookOpenText, ChevronsUpDown, FileText, Layers3, LibraryBig, PanelLeftClose, ScrollText, Sparkles, SplitSquareVertical } from "lucide-react";
import type { ArtifactType, Project, Run, ViewMode } from "../../api/types";
import { sourceModeLabel, runStatusLabel, type NavItem } from "../../lib/workbenchLabels";

interface ProjectSidebarProps {
  run: Run | null;
  project: Project | null;
  navItems: NavItem[];
  view: ViewMode;
  selectedArtifactType: ArtifactType | null;
  onSelect: (view: ViewMode, artifactType: ArtifactType | null) => void;
  onCollapse: () => void;
  onOpenProjects: () => void;
}

export function ProjectSidebar({ run, project, navItems, view, selectedArtifactType, onSelect, onCollapse, onOpenProjects }: ProjectSidebarProps) {
  return (
    <aside className="sidebar-pane" id="project-sidebar">
      <div className="project-card">
        <button className="project-card-copy project-switch-button" onClick={onOpenProjects} title="切换作品" type="button">
          <div className="project-name">{project?.title || "未选择作品"}</div>
          <div className="project-meta">{run ? `${sourceModeLabel(run.source_mode)} · ${runStatusLabel(run.status)}` : "等待输入材料"}</div>
          <ChevronsUpDown aria-hidden size={14} />
        </button>
        <button aria-controls="project-sidebar" aria-expanded="true" aria-label="关闭目录面板" className="panel-collapse-button" onClick={onCollapse} title="关闭目录面板" type="button">
          <PanelLeftClose size={16} />
        </button>
      </div>
      <nav className="nav-list" aria-label="project artifacts">
        {navItems.map((item) => (
          <button
            className={`nav-item ${view === item.view && selectedArtifactType === item.artifactType ? "active" : ""}`}
            key={`${item.view}:${item.artifactType || item.label}`}
            onClick={() => onSelect(item.view, item.artifactType)}
            type="button"
          >
            <span className="nav-label">
              <NavIcon artifactType={item.artifactType} view={item.view} />
              <span>{item.label}</span>
            </span>
            <span className={`nav-status ${statusClass(item.status)}`}>{item.status}</span>
          </button>
        ))}
      </nav>
    </aside>
  );
}

function NavIcon({ artifactType, view }: { artifactType: ArtifactType | null; view: ViewMode }) {
  const Icon =
    view === "events"
      ? Activity
      : view === "script"
        ? ScrollText
        : artifactType === "source_input"
          ? FileText
          : artifactType === "story_bible"
            ? BookOpenText
            : artifactType === "episode_split"
              ? SplitSquareVertical
              : artifactType === "material_bank"
                ? LibraryBig
                : artifactType === "story_seed"
                  ? Sparkles
                  : Layers3;
  return <Icon aria-hidden="true" size={15} strokeWidth={1.8} />;
}

function statusClass(status: string) {
  if (status.includes("失败")) return "danger";
  if (status.includes("确认")) return "warning";
  if (status.includes("生成中") || status.includes("运行")) return "active";
  if (status.includes("已生成") || status.includes("已完成")) return "success";
  return "neutral";
}
