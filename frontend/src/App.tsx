import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import {
  ArrowLeft,
  ArrowDown,
  ArrowUp,
  Bot,
  BookOpenText,
  Check,
  ChevronDown,
  CircleAlert,
  Clapperboard,
  ClipboardCheck,
  Clock3,
  Download,
  FileText,
  Filter,
  FolderOpen,
  History,
  LoaderCircle,
  LogIn,
  MessageSquare,
  MoreHorizontal,
  Paperclip,
  Pencil,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Search,
  Settings2,
  Square,
  Sparkles,
  Trash2,
  X,
} from "lucide-react";
import { api, ApiError, fileKind, messageClientInstanceID } from "./api";
import { composerSkillIdentity, messageSubmission, MessageStorageError, starterContext, type MessageOwner, type SavedMessage, type SendMessage } from "./messageSubmission";
import { nonBatchAttachmentRefs, reconcileVideoBatchAttachments, removeAttachmentReferences, videoBatchAttachmentRefs } from "./batchAttachments";
import { createUUID } from "./uuid";
import { runActionIdentity, runActionMatchesSnapshot } from "./runActions";
import { ArtifactDocument } from "./artifacts/ArtifactDocument";
import { ArtifactDownloads } from "./artifacts/ArtifactDownloads";
import type { VersionResult } from "./types";
import { DocumentArtifactEditor } from "./artifacts/DocumentArtifactEditor";
import { ScriptArtifactEditor } from "./artifacts/ScriptArtifactEditor";
import { VideoScriptArtifactEditor } from "./artifacts/VideoScriptArtifactEditor";
import { artifactUsesDirectEditor, rendererForArtifact } from "./artifacts/artifactRendererRegistry";
import { ScriptCollectionView } from "./artifacts/ScriptCollectionView";
import { presentationFor } from "./artifacts/artifactPresentation";
import { artifactIsVisible, compareArtifacts, groupArtifactsForRail } from "./artifacts/artifactNavigation";
import { ProcessHistory, TimelineMessage } from "./components/agent/AgentHistory";
import { AgentExecutionHistory } from "./components/agent/AgentExecutionHistory";
import { ProjectGoalSummary } from "./components/agent/ProjectGoalSummary";
import { ProjectMemory } from "./components/agent/ProjectMemory";
import { AgentTaskInputs, AgentTurnInputs } from "./components/agent/AgentTaskInputs";
import { StatefulRunInputs } from "./components/agent/StatefulRunInputs";
import { ScriptEditRecovery } from "./components/agent/ScriptEditRecovery";
import type { PendingScriptEdit } from "./types";
import { AgentToolExecutionFacts, executionLabel } from "./components/agent/AgentToolExecutionFacts";
import { ProjectFiles } from "./components/files/ProjectFiles";
import { ProjectMaterialPicker } from "./components/files/ProjectMaterialPicker";
import { materialAttachment, materialUnavailableReason } from "./components/files/projectMaterials";
import { SkillManagement } from "./components/skills/SkillManagement";
import { AgentInstructions } from "./components/agent/AgentInstructions";
import { InstructionChangeApproval } from "./components/agent/InstructionChangeApproval";
import { MemoryChangeApproval } from "./components/agent/MemoryChangeApproval";
import { MemoryToolApproval } from "./components/agent/MemoryToolApproval";
import { JsonSchemaForm, schemaCanUseForm, schemaDefaults, schemaRequiresMaterial, schemaValueIsValid } from "./components/skills/JsonSchemaForm";
import { buildConversationTimeline, revisionBlocksApproval } from "./conversationTimeline";
import type { ApprovalRevisionTarget } from "./types";
import { createOptimisticUserMessage, markOptimisticMessageUnconfirmed, markOptimisticMessageFailed, mergeSnapshotMessages, settleOptimisticMessage } from "./optimisticMessage";
import { presentConversationMessages } from "./messagePresentation";
import { agentTurnMessage, agentTurnReceiptMatches, agentTurnSubmissionID, mergeMessagesWithAgentTurns } from "./agentTurns";
import { agentEventIsTerminal, parseAgentEvent, type AgentEventEnvelope } from "./agentEventProtocol";
import { stepLabels } from "./presentation";
import { currentStepTasks, executionRecovery, taskIsCompleted, taskIsRetrying, taskProgressLabel, taskProgressTitle, taskRuntimeStatusLabel } from "./taskPresentation";
import { canonicalJSONStringify, episodeNumberFromScope, resolveViewedCapabilityID, targetSummary } from "./contextTargeting";
import { artifactPresentationsFromRegistries, capabilityAccepts, capabilityDependenciesReady, capabilityEntry, capabilityLabel, resolveAgentToolInteraction, resolveApprovalViewKey, resolveTaskViewKey, trustedComposerViewKey, trustedConfigViewKey, type AgentToolViewKey, type ApprovalViewKey, type TaskViewKey } from "./workspaceProjection";
import type { AdaptationOptions, AdaptationResolutionPayload, AgentComposerContext, AgentTargetSelection, AgentTask, AgentToolApproval, AgentToolCall, AgentTurn, Approval, Artifact, ArtifactPresentation, ArtifactVersion, Asset, AssetSetSnapshot, AttachmentRef, AvailableAction, CapabilityDefinition, ComposerRegistryEntry, ContinuationOptions, FinalSelection, Message, Principal, Project, ProjectActivity, ProjectDeletePreview, ProposedAction, QualityReview, RevisionRequest, RunSnapshot, ScriptCandidate, TargetResolution, TaskItem, WorkspaceRegistries } from "./types";

const maxVideoUploadBytes = 500 * 1024 * 1024;
const maxArchiveUploadBytes = 1024 * 1024 * 1024;

type AgentTurnLiveState = { status: string; output: string };

function safeAgentToolStatus(toolName: unknown): string {
  const name = typeof toolName === "string" ? toolName : "";
  if (name === "programmatic_tool_calling") return "正在执行程序化工具调用";
  if (name === "list_capabilities" || name === "load_skill_instructions") return "正在匹配 Skill";
  if (name.includes("artifact")) return "正在检查产物";
  if (name.includes("asset") || name.includes("material")) return "正在读取材料";
  if (name === "commit_agent_action") return "正在保存结果";
  return "正在调用工具";
}

export function nextAgentTurnLiveState(
  current: AgentTurnLiveState | undefined, event: AgentEventEnvelope,
): AgentTurnLiveState {
  const previous = current ?? { status: "正在处理", output: "" };
  if (event.event_type === "agent.turn.started") return { ...previous, status: "正在理解请求" };
  if (event.event_type === "agent.tool.started") return { ...previous, status: safeAgentToolStatus(event.payload.tool_name ?? event.payload.name) };
  if (event.event_type === "agent.tool.completed") return { ...previous, status: event.payload.tool_name === "programmatic_tool_calling" && event.payload.status === "incomplete" ? "程序执行未完成" : "正在整理结果" };
  if (event.event_type === "agent.approval.requested") return { ...previous, status: "等待确认" };
  if (event.event_type === "agent.turn.waiting_approval") return { ...previous, status: "等待工具授权" };
  if (event.event_type === "agent.turn.pause_requested") return { ...previous, status: "等待当前轮完成后暂停" };
  if (event.event_type === "agent.turn.paused") return { ...previous, status: "本轮已暂停" };
  if (event.event_type === "agent.turn.resume_requested") return { ...previous, status: event.payload.status === "waiting_approval" ? "等待工具授权" : "等待恢复执行" };
  if (event.event_type === "agent.artifact.created") return { ...previous, status: "已创建产物" };
  if (event.event_type === "agent.output.delta") return { ...previous, status: "正在生成回复", output: previous.output + (typeof event.payload.text === "string" ? event.payload.text : "") };
  if (event.event_type === "agent.turn.committed") return { ...previous, status: "已完成" };
  if (event.event_type === "agent.turn.cancelled") return { ...previous, status: "已停止" };
  if (event.event_type === "agent.turn.failed") return { ...previous, status: "处理失败" };
  return previous;
}

function acceptedUploadTypes(capability: ComposerRegistryEntry | null): string {
  if (!capability) return ".txt,.md,.csv,.json,.docx,.pdf,.jpg,.jpeg,.png,.webp,.mp4,.mov,.zip";
  const mapping: Record<string, string[]> = {
    text: [".txt", ".md", ".csv", ".json"], document: [".docx", ".pdf"], image: [".jpg", ".jpeg", ".png", ".webp"], video: [".mp4", ".mov"],
  };
  return [...new Set(capability.accepted_asset_kinds.flatMap((kind) => mapping[kind] ?? [])), ".zip"].join(",");
}

export function episodeNoFromFilename(filename: string): number | null {
  const normalized = filename.replace(/[０-９]/g, (digit) => String(digit.charCodeAt(0) - 0xFF10));
  const matches = [...normalized.matchAll(/第\s*(\d+)\s*集/g)]
    .map((match) => Number(match[1]))
    .filter((value) => Number.isInteger(value) && value > 0);
  return new Set(matches).size === 1 ? matches[0] : null;
}

export function nextSkillPrompt(currentValue: string, previousAutoPrompt: string | null, capability: Pick<ComposerRegistryEntry, "default_prompt">) {
  const prompt = capability.default_prompt;
  if (!prompt || (currentValue.trim() && currentValue !== previousAutoPrompt)) return { value: currentValue, autoPrompt: previousAutoPrompt };
  return { value: prompt, autoPrompt: prompt };
}


function navigate(path: string) {
  history.pushState({}, "", path);
  window.dispatchEvent(new PopStateEvent("popstate"));
}

function formatTime(value: string) {
  const date = new Date(value);
  const now = new Date();
  if (date.toDateString() === now.toDateString()) return `今天 ${date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" })}`;
  return date.toLocaleDateString("zh-CN", { month: "numeric", day: "numeric" });
}

function errorText(error: unknown) {
  return error instanceof ApiError || error instanceof MessageStorageError ? error.message : "暂时无法连接服务，请检查后端是否已启动。";
}

function useModalKeyboard() {
  useEffect(() => {
    const dialog = document.querySelector<HTMLElement>(".modal-layer section");
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (!dialog) return;
    const focusable = () => Array.from(dialog.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])'));
    focusable()[0]?.focus();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") { event.preventDefault(); dialog.querySelector<HTMLButtonElement>('button[aria-label="关闭"]')?.click(); return; }
      if (event.key !== "Tab") return;
      const items = focusable();
      if (!items.length) return;
      const first = items[0]; const last = items.at(-1)!;
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    };
    dialog.addEventListener("keydown", onKeyDown);
    return () => { dialog.removeEventListener("keydown", onKeyDown); previous?.focus(); };
  });
}

function projectStatus(project: Project) {
  const status = project.latest_run_status ?? project.status;
  const labels: Record<string, string> = { pending: "准备中", running: "生成中", waiting_approval: "等待确认", pausing: "暂停中", paused: "已暂停", failed: "失败", completed: "已完成", cancelled: "已结束", active: "待处理", ready: "待处理" };
  return labels[status] ?? "待处理";
}

function projectStatusTone(project: Project) {
  const status = project.latest_run_status ?? project.status;
  if (["pending", "running", "pausing"].includes(status)) return "running";
  if (["failed", "cancelled"].includes(status)) return "warning";
  return "neutral";
}

const PrincipalContext = createContext<Principal | null>(null);
const RefreshPrincipalContext = createContext<(force?: boolean) => Promise<void>>(async () => {});

function usePrincipal() {
  const principal = useContext(PrincipalContext);
  if (!principal) throw new Error("principal context is unavailable");
  return principal;
}

export function App() {
  const [principal, setPrincipal] = useState<Principal | null | undefined>(undefined);
  const [token, setToken] = useState("");
  const [failure, setFailure] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const auth = useRef({ mounted: false, revision: 0, inFlight: 0, loggingIn: false, cookieSession: false, principal: undefined as Principal | null | undefined });

  const refreshPrincipal = useCallback(async (force = false) => {
    if (!auth.current.mounted || auth.current.loggingIn || (auth.current.inFlight && !force)) return;
    const revision = ++auth.current.revision;
    auth.current.inFlight = revision;
    try {
      const value = await api.getCurrentPrincipal();
      if (!auth.current.mounted || revision !== auth.current.revision) return;
      if (auth.current.cookieSession && value.auth_method !== "session_cookie") {
        throw new ApiError("AUTHENTICATION_REQUIRED", "当前会话已失效，请重新登录。", 401);
      }
      auth.current.principal = value;
      auth.current.cookieSession = value.auth_method === "session_cookie";
      setPrincipal(value);
      setFailure("");
    } catch (error) {
      if (!auth.current.mounted || revision !== auth.current.revision) return;
      if (!auth.current.principal || (error instanceof ApiError && [401, 403].includes(error.status))) {
        const hadPrincipal = Boolean(auth.current.principal);
        auth.current.principal = null;
        auth.current.cookieSession = false;
        setPrincipal(null);
        setFailure(error instanceof ApiError && error.status === 401 ? hadPrincipal ? "当前会话已失效，请重新登录。" : "" : errorText(error));
      }
    } finally {
      if (auth.current.inFlight === revision) auth.current.inFlight = 0;
    }
  }, []);

  useEffect(() => {
    auth.current.mounted = true;
    void refreshPrincipal();
    const refreshVisible = () => {
      if (auth.current.principal && document.visibilityState !== "hidden") void refreshPrincipal();
    };
    window.addEventListener("focus", refreshVisible);
    window.addEventListener("popstate", refreshVisible);
    document.addEventListener("visibilitychange", refreshVisible);
    const timer = window.setInterval(refreshVisible, 30_000);
    return () => {
      auth.current.mounted = false;
      ++auth.current.revision;
      auth.current.inFlight = 0;
      window.clearInterval(timer);
      window.removeEventListener("focus", refreshVisible);
      window.removeEventListener("popstate", refreshVisible);
      document.removeEventListener("visibilitychange", refreshVisible);
    };
  }, [refreshPrincipal]);

  const login = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!token.trim() || submitting || auth.current.loggingIn) return;
    const revision = ++auth.current.revision;
    auth.current.loggingIn = true;
    auth.current.inFlight = 0;
    setSubmitting(true); setFailure("");
    try {
      const value = await api.createAuthSession(token.trim());
      if (!auth.current.mounted || revision !== auth.current.revision) return;
      setToken("");
      auth.current.principal = value;
      // The login response describes bearer authentication; it also sets a cookie.
      auth.current.cookieSession = true;
      setPrincipal(value);
    } catch (error) {
      if (auth.current.mounted && revision === auth.current.revision) setFailure(errorText(error));
    } finally {
      if (auth.current.mounted && revision === auth.current.revision) {
        auth.current.loggingIn = false;
        setSubmitting(false);
      }
    }
  };

  if (principal === undefined) {
    return <main className="auth-page" aria-busy="true"><LoaderCircle className="spin" size={24} /><span>正在验证会话</span></main>;
  }
  if (!principal) {
    return <main className="auth-page">
      <form className="auth-form" onSubmit={(event) => void login(event)}>
        <div className="auth-brand"><span className="brand-mark">C</span><strong>内容生产</strong></div>
        <h1>登录工作区</h1>
        <label htmlFor="access-token">访问令牌</label>
        <input id="access-token" type="password" autoComplete="current-password" autoFocus value={token} onChange={(event) => setToken(event.target.value)} />
        {failure && <p className="form-error" role="alert">{failure}</p>}
        <button className="primary-button" type="submit" disabled={!token.trim() || submitting}>
          {submitting ? <LoaderCircle className="spin" size={15} /> : <LogIn size={15} />}
          {submitting ? "正在登录" : "登录"}
        </button>
      </form>
    </main>;
  }
  return <RefreshPrincipalContext.Provider value={refreshPrincipal}><PrincipalContext.Provider value={principal}><RoutedApp key={JSON.stringify([principal.workspace_id, principal.user_id])} /></PrincipalContext.Provider></RefreshPrincipalContext.Provider>;
}

function RoutedApp() {
  const [path, setPath] = useState(location.pathname);
  useEffect(() => {
    const onRoute = () => setPath(location.pathname);
    window.addEventListener("popstate", onRoute);
    return () => window.removeEventListener("popstate", onRoute);
  }, []);
  const match = path.match(/^\/projects\/([^/]+)$/);
  const principal = usePrincipal();
  if (path === "/skills") return <SkillManagement key={JSON.stringify([principal.workspace_id, principal.user_id])} workspaceName={principal.workspace_name} role={principal.role} userID={principal.user_id} workspaceID={principal.workspace_id} />;
  if (path === "/instructions") return <AgentInstructions workspaceName={principal.workspace_name} initialProjectID={new URLSearchParams(location.search).get("project_id") ?? ""} />;
  return match ? <Workbench key={match[1]} projectID={match[1]} /> : <ProjectEntry />;
}

function BrandHeader({ onCreate }: { onCreate?: () => void }) {
  const principal = usePrincipal();
  return (
    <header className="global-header">
      <a className="brand" href="/" aria-label="返回作品列表">
        <span className="brand-mark">C</span><span>内容生产</span>
      </a>
      <div className="header-actions">
        <span className="workspace-label" title={`${principal.display_name} · ${principal.role}`}>{principal.workspace_name} · {principal.display_name}</span>
        <a className="header-link" href="/skills">Skill 管理</a>
        <a className="header-link" href="/instructions">Agent 规则</a>
        {onCreate && <button className="result-button" onClick={onCreate}>新建作品<span><Plus size={14} /></span></button>}
      </div>
    </header>
  );
}

function usePrincipalRequestScope(principal: Pick<Principal, "workspace_id" | "user_id" | "role">) {
  const key = JSON.stringify([principal.workspace_id, principal.user_id, principal.role]);
  const scope = useRef({ key, generation: 0, active: false });
  if (scope.current.key !== key) {
    scope.current.key = key;
    ++scope.current.generation;
  }
  useEffect(() => {
    scope.current.active = true;
    return () => { scope.current.active = false; ++scope.current.generation; };
  }, []);
  return scope;
}

function ProjectEntry() {
  useModalKeyboard();
  const principal = usePrincipal();
  const requestScope = usePrincipalRequestScope(principal);
  const readOnly = principal.role === "viewer";
  const [projects, setProjects] = useState<Project[]>([]);
  const [capabilities, setCapabilities] = useState<ComposerRegistryEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [failure, setFailure] = useState("");
  const [search, setSearch] = useState("");
  const [dialog, setDialog] = useState<"create" | "rename" | "delete" | null>(null);
  const [target, setTarget] = useState<Project | null>(null);
  const [menuID, setMenuID] = useState<string | null>(null);
  const [deletePreview, setDeletePreview] = useState<ProjectDeletePreview | null>(null);
  useEffect(() => {
    if (!menuID) return;
    const close = (event: PointerEvent) => {
      if (!(event.target as Element).closest(`[data-project-menu="${menuID}"]`)) setMenuID(null);
    };
    const escape = (event: KeyboardEvent) => event.key === "Escape" && setMenuID(null);
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", escape);
    return () => { document.removeEventListener("pointerdown", close); document.removeEventListener("keydown", escape); };
  }, [menuID]);

  const load = async () => {
    const generation = requestScope.current.generation;
    const current = () => requestScope.current.active && requestScope.current.generation === generation;
    setLoading(true); setFailure("");
    try {
      const [projectItems, projection] = await Promise.all([api.listProjects(), api.getWorkspaceProjection()]);
      if (!current()) return;
      setProjects(projectItems);
      setCapabilities(projection.registries.composer.filter((item) => item.status !== "unavailable"));
    } catch (error) { if (current()) setFailure(errorText(error)); }
    finally { if (current()) setLoading(false); }
  };
  useEffect(() => { void load(); }, [principal.workspace_id, principal.user_id, principal.role]);

  const visible = useMemo(() => projects.filter((item) => item.title.toLowerCase().includes(search.toLowerCase())), [projects, search]);
  const openDialog = (kind: typeof dialog, project: Project | null = null) => { if (readOnly) return; setTarget(project); setDialog(kind); setMenuID(null); setDeletePreview(null); };
  const closeDialog = () => { setDialog(null); setTarget(null); setDeletePreview(null); };

  async function submitName(title: string) {
    if (readOnly) throw new ApiError("ROLE_FORBIDDEN", "当前账号无编辑权限。", 403);
    const generation = requestScope.current.generation;
    const current = () => requestScope.current.active && requestScope.current.generation === generation;
    if (dialog === "create") {
      const created = await api.createProject(title);
      if (!current()) return;
      closeDialog(); navigate(`/projects/${created.project_id}`);
      return;
    }
    if (dialog === "rename" && target) {
      const updated = await renameProjectWithCurrentVersion(target.project_id, title, current);
      if (!current()) return;
      setProjects((items) => items.map((item) => item.project_id === updated.project_id ? updated : item));
      closeDialog();
    }
  }

  async function beginDelete() {
    if (!target || readOnly) return;
    const generation = requestScope.current.generation;
    const preview = await api.previewProjectDelete(target.project_id);
    if (requestScope.current.active && requestScope.current.generation === generation) setDeletePreview(preview);
  }
  async function confirmDelete() {
    if (!target || !deletePreview || readOnly) return;
    const generation = requestScope.current.generation;
    await api.confirmProjectDelete(target.project_id, deletePreview.snapshot_hash);
    if (!requestScope.current.active || requestScope.current.generation !== generation) return;
    setProjects((items) => items.filter((item) => item.project_id !== target.project_id));
    closeDialog();
  }

  return (
    <div className="entry-page">
      <a className="skip-link" href="#project-main">跳到主要内容</a>
      <BrandHeader onCreate={readOnly ? undefined : () => openDialog("create")} />
      <main className="project-workspace" id="project-main">
        <NewProjectStarter capabilities={capabilities} principal={principal} />
        <section className="recent-projects" aria-labelledby="recent-projects-title">
          <div className="page-heading">
          <div><h2 id="recent-projects-title">最近作品</h2><p>每个作品拥有独立的对话、素材和产物上下文。</p></div>
          <div className="filters">
            <label className="search-control"><Search size={15} /><input name="project-search" autoComplete="off" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索作品…" /></label>
            <button className="icon-button" aria-label="筛选作品" title="筛选作品"><Filter size={16} /></button>
          </div>
        </div>
        {failure ? <ConnectionFailure message={failure} retry={load} /> : loading ? <div className="loading-row">正在读取作品…</div> : visible.length === 0 ? (
          <div className="empty-state"><FolderOpen size={25} /><h2>{search ? "没有匹配的作品" : "还没有作品"}</h2><p>{search ? "换一个关键词试试。" : "新建作品后，Agent 会在独立上下文中继续工作。"}</p></div>
        ) : (
          <div className="project-table" role="table" aria-label="作品列表">
            <div className="project-row table-head" role="row"><span>作品</span><span>当前链路</span><span>当前状态</span><span>最近更新</span><span /></div>
            {visible.map((project) => (
              <div className="project-row" role="row" key={project.project_id} onClick={() => navigate(`/projects/${project.project_id}`)}>
                <strong><a className="project-link" href={`/projects/${project.project_id}`} onClick={(event) => { event.preventDefault(); event.stopPropagation(); navigate(`/projects/${project.project_id}`); }}>{project.title}</a></strong>
                <span>{project.current_capability_id || project.latest_capability_id ? capabilityLabel(capabilities, project.current_capability_id ?? project.latest_capability_id, project.current_capability_id ?? project.latest_capability_id ?? "尚未选择") : "尚未选择"}</span>
                <span className={`status-text ${projectStatusTone(project)}`}>{projectStatus(project)}</span>
                <span className="muted">{formatTime(project.updated_at)}</span>
                <div className="row-action" data-project-menu={project.project_id} onClick={(event) => event.stopPropagation()}>
                  {!readOnly && <>
                  <button className="icon-button" aria-label={`打开 ${project.title} 的操作菜单`} title="更多操作" onClick={() => setMenuID(menuID === project.project_id ? null : project.project_id)}><MoreHorizontal size={17} /></button>
                  {menuID === project.project_id && <div className="anchor-menu"><button onClick={() => openDialog("rename", project)}>重命名</button><button className="danger" onClick={() => openDialog("delete", project)}><Trash2 size={15} />删除作品</button></div>}
                  </>}
                </div>
              </div>
            ))}
          </div>
        )}
        </section>
      </main>
      {!readOnly && (dialog === "create" || dialog === "rename") && <NameDialog mode={dialog} initialValue={target?.title ?? ""} onClose={closeDialog} onSubmit={submitName} />}
      {!readOnly && dialog === "delete" && target && <DeleteDialog project={target} preview={deletePreview} onClose={closeDialog} onPreview={beginDelete} onConfirm={confirmDelete} />}
    </div>
  );
}

export async function renameProjectWithCurrentVersion(projectID: string, title: string, isCurrent: () => boolean = () => true) {
  // Runs update the project version while the list is open. Resolve the
  // current version immediately before applying this user edit.
  if (!isCurrent()) throw new ApiError("ROLE_FORBIDDEN", "作品或操作权限已变化，请刷新后核对。", 403);
  let current = await api.getProject(projectID);
  if (!isCurrent()) throw new ApiError("ROLE_FORBIDDEN", "作品或操作权限已变化，请刷新后核对。", 403);
  try {
    return await api.renameProject(current, title);
  } catch (error) {
    if (!(error instanceof ApiError) || error.code !== "PROJECT_VERSION_CONFLICT") throw error;
    if (!isCurrent()) throw new ApiError("ROLE_FORBIDDEN", "作品或操作权限已变化，请刷新后核对。", 403);
    current = await api.getProject(projectID);
    if (!isCurrent()) throw new ApiError("ROLE_FORBIDDEN", "作品或操作权限已变化，请刷新后核对。", 403);
    return api.renameProject(current, title);
  }
}

const starterSkillIcons: Record<string, typeof Bot> = {
  book_open_text: BookOpenText,
  file_text: FileText,
  clapperboard: Clapperboard,
  clipboard_check: ClipboardCheck,
  search: Search,
  sparkles: Sparkles,
};

export function projectTitleFromPrompt(prompt: string) {
  const normalized = prompt.replace(/\s+/g, " ").trim().replace(/^[，。！？、,.!?\s]+/, "");
  if (!normalized) return "未命名作品";
  return normalized.length > 24 ? `${normalized.slice(0, 24)}…` : normalized;
}

const pendingMessageCache = new Map<string, Message & { submissionKey?: string; transportID?: string }>();
const pendingMessageKey = (principal: Pick<Principal, "workspace_id" | "user_id">, projectID: string) => JSON.stringify([principal.workspace_id, principal.user_id, projectID]);

function saveStarterHandoff(owner: MessageOwner, handoff: { content: string; capability: ComposerRegistryEntry | null; attachments: AttachmentRef[]; video_batch: AssetSetSnapshot | null }) {
  const context = starterContext(owner.project_id, handoff.capability, handoff.video_batch);
  const saved = messageSubmission.prepare(owner, { ...handoff, context, clientInstanceID: messageClientInstanceID() }, "starter");
  const pending = createOptimisticUserMessage(handoff.content, context);
  pending.message_context = {
    ...pending.message_context!,
    attachment_refs: handoff.attachments,
  };
  pendingMessageCache.set(pendingMessageKey(owner, owner.project_id), { ...pending, submissionKey: saved.key });
}

export function NewProjectStarter({ capabilities, principal }: { capabilities: ComposerRegistryEntry[]; principal: Pick<Principal, "workspace_id" | "user_id" | "role"> }) {
  const requestScope = usePrincipalRequestScope(principal);
  const readOnly = principal.role === "viewer";
  const [value, setValue] = useState("");
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [autoPrompt, setAutoPrompt] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const [videoBatch, setVideoBatch] = useState<AssetSetSnapshot | null>(null);
  const [batchOpen, setBatchOpen] = useState(false);
  const [batchPending, setBatchPending] = useState(false);
  const submitting = useRef(false);
  const draftProject = useRef<Project | null>(null);
  const preparedAttachments = useRef<AttachmentRef[]>([]);
  const batchAppendCommand = useRef<{ signature: string; key: string } | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const uploadedFiles = useRef(new WeakSet<File>());
  const fileAttachments = useRef(new WeakMap<File, AttachmentRef[]>());
  const selected = capabilities.find((item) => item.capability_id === selectedID) ?? null;

  const choose = (capabilityID: string | null) => {
    if (submitting.current || batchPending) return;
    setSelectedID(capabilityID);
    setFailure("");
    if (!capabilityID) {
      setValue((current) => current === autoPrompt ? "" : current);
      setAutoPrompt(null);
      return;
    }
    setValue((current) => {
      const capability = capabilities.find((item) => item.capability_id === capabilityID);
      if (!capability) return current;
      const next = nextSkillPrompt(current, autoPrompt, capability);
      setAutoPrompt(next.autoPrompt);
      return next.value;
    });
  };

  const submit = async () => {
    const content = value.trim();
    if (!content || busy || submitting.current || batchPending || principal.role === "viewer") return;
    submitting.current = true;
    const generation = requestScope.current.generation;
    const current = () => requestScope.current.active && requestScope.current.generation === generation;
    setBusy(true); setFailure("");
    try {
      const project = draftProject.current ?? await api.createProject(projectTitleFromPrompt(content));
      if (!current()) return;
      draftProject.current = project;
      const pendingFiles = files.filter((file) => !uploadedFiles.current.has(file));
      for (const file of pendingFiles) {
        const uploaded = await api.uploadFiles(project.project_id, [file]);
        if (!current()) return;
        uploadedFiles.current.add(file);
        fileAttachments.current.set(file, uploaded);
        preparedAttachments.current = [...preparedAttachments.current, ...uploaded];
      }
      const attachments = preparedAttachments.current;
      if (selected && trustedComposerViewKey(selected.view_key) === "video_asset_set") {
        const videos = attachments.filter((item) => item.kind === "video" || fileKind({ name: item.display_name, type: "" }) === "video");
        if (!videos.length) {
          setFailure("视频参考创作需要先添加视频文件，ZIP 中未发现可用视频。");
          setBusy(false);
          return;
        }
        const missing = videos.filter((item) => !videoBatch?.members.some((member) => member.asset_id === item.asset_id));
        const signature = canonicalJSONStringify([project.project_id, videoBatch?.version.asset_set_version_id, missing.map((item) => item.asset_id), selected.input_binding?.asset_set_purpose]);
        if (batchAppendCommand.current?.signature !== signature) batchAppendCommand.current = { signature, key: createUUID() };
        const batch = videoBatch && !missing.length ? videoBatch : await api.appendVideoBatch(project.project_id, videoBatch, missing, selected.input_binding?.asset_set_purpose, batchAppendCommand.current.key);
        if (!current()) return;
        if (!videoBatchMatches(batch, project.project_id, videoBatch?.asset_set.asset_set_id ?? batch.asset_set.asset_set_id)) throw new ApiError("ASSET_SET_RECEIPT_INVALID", "视频批次回执与当前作品不一致。", 502);
        setVideoBatch(batch);
        setBatchOpen(true);
        setBusy(false);
        return;
      }
      saveStarterHandoff({ workspace_id: principal.workspace_id, user_id: principal.user_id, project_id: project.project_id, conversation_id: project.primary_conversation_id }, { content, capability: selected, attachments, video_batch: null });
      navigate(`/projects/${project.project_id}`);
    } catch (error) {
      if (current()) setFailure(errorText(error));
    } finally {
      submitting.current = false; if (requestScope.current.active) setBusy(false);
    }
  };

  const continueWithVideoBatch = async (next: AssetSetSnapshot) => {
    if (!draftProject.current || principal.role === "viewer") return;
    const generation = requestScope.current.generation;
    const attachments = await reconcileVideoBatchAttachments(draftProject.current.project_id, videoBatch, next, preparedAttachments.current);
    if (!requestScope.current.active || requestScope.current.generation !== generation) return;
    preparedAttachments.current = attachments;
    setVideoBatch(next);
    if (next.asset_set.status !== "sealed") return;
    saveStarterHandoff({ workspace_id: principal.workspace_id, user_id: principal.user_id, project_id: draftProject.current.project_id, conversation_id: draftProject.current.primary_conversation_id }, { content: value.trim(), capability: selected, attachments: videoBatchAttachmentRefs(next, attachments), video_batch: next });
    navigate(`/projects/${draftProject.current.project_id}`);
  };

  const removeFile = (file: File, index: number) => {
    if (submitting.current || batchPending) return;
    const next = removeAttachmentReferences(preparedAttachments.current, (fileAttachments.current.get(file) ?? []).map((item) => item.asset_id));
    if (videoBatch?.members.some((member) => !next.some((item) => item.asset_id === member.asset_id))) setVideoBatch(null);
    preparedAttachments.current = next;
    uploadedFiles.current.delete(file); fileAttachments.current.delete(file);
    setFiles((current) => current.filter((_, itemIndex) => itemIndex !== index));
  };

  const selectFiles = (list: FileList | null) => {
    if (!list?.length || busy || batchPending || submitting.current) return;
    const incoming = Array.from(list);
    const invalidVideo = incoming.find((file) => fileKind(file) === "video" && file.size > maxVideoUploadBytes);
    const invalidArchive = incoming.find((file) => fileKind(file) === "archive" && file.size > maxArchiveUploadBytes);
    const unsupported = selected && incoming.find((file) => fileKind(file) !== "archive" && !capabilityAccepts(selected, fileKind(file)));
    if (invalidVideo) { setFailure(`${invalidVideo.name} 超过 500 MB，请压缩后重试。`); return; }
    if (invalidArchive) { setFailure(`${invalidArchive.name} 超过 1 GB，请拆分后重试。`); return; }
    if (unsupported) { setFailure(`${selected.label} 不接受 ${unsupported.name} 这类文件。`); return; }
    setFiles((current) => [...current, ...incoming]);
    setFailure("");
    if (fileInput.current) fileInput.current.value = "";
  };

  return (
    <section className="project-starter" aria-labelledby="project-starter-title">
      <div className="starter-heading">
        <div><span className="starter-kicker">新作品</span><h1 id="project-starter-title">从一个想法开始</h1></div>
        <p>选择通用 Agent 自由协作，或明确指定一条 Skill。</p>
      </div>
      <div className="starter-skills" role="radiogroup" aria-label="创作方式">
        <button type="button" role="radio" aria-checked={!selectedID} disabled={busy || batchPending} className={`starter-skill ${!selectedID ? "selected" : ""}`} onClick={() => choose(null)}>
          <span className="starter-skill-icon"><Bot size={18} /></span><span><strong>通用 Agent</strong><small>自由讨论与创作</small></span><Check size={15} className="starter-check" />
        </button>
        {capabilities.map((capability) => {
          const Icon = starterSkillIcons[capability.icon_key] ?? Sparkles;
          const active = selectedID === capability.capability_id;
          const supported = trustedComposerViewKey(capability.view_key) !== "inspector" && capabilityDependenciesReady(capability);
          return <button type="button" role="radio" aria-checked={active} disabled={!supported || busy || batchPending} title={!supported ? capability.user_message || "请先在 Skill 管理中检查视图或依赖。" : undefined} className={`starter-skill ${active ? "selected" : ""}`} key={capability.capability_id} onClick={() => choose(capability.capability_id)}><span className="starter-skill-icon"><Icon size={18} /></span><span><strong>{capability.label}</strong><small>{supported ? capability.description : "只读检查 · 当前不可调用"}</small></span><Check size={15} className="starter-check" /></button>;
        })}
      </div>
      <div className="starter-composer">
        <textarea aria-label="新作品要求" disabled={readOnly || busy || batchPending} value={value} onChange={(event) => setValue(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); void submit(); } }} placeholder={selected ? `描述你希望“${selected.label}”完成的内容…` : "描述你想创作、分析或修改的内容…"} maxLength={4000} />
        {files.length > 0 && <div className="starter-attachments" aria-label="作品附件">{files.map((file, index) => <span key={`${file.name}-${file.size}-${index}`} title={file.name}><FileText size={13} /><span>{file.name}</span><button type="button" aria-label={`移除 ${file.name}`} disabled={busy || batchPending} onClick={() => removeFile(file, index)}><X size={12} /></button></span>)}</div>}
        <div className="starter-composer-footer">
          <div><input ref={fileInput} className="visually-hidden" type="file" multiple disabled={readOnly || busy || batchPending} accept={acceptedUploadTypes(selected)} onChange={(event) => selectFiles(event.target.files)} /><button type="button" className="starter-attach" aria-label="添加文件" title="添加文件" disabled={readOnly || busy || batchPending} onClick={() => fileInput.current?.click()}><Paperclip size={16} /></button><span>{selected ? `已选择 ${selected.label}` : "通用 Agent"}</span></div>
          <button type="button" className="starter-submit" disabled={readOnly || !value.trim() || busy || batchPending} onClick={() => void submit()} aria-label="创建作品并发送">
            {busy ? <LoaderCircle size={17} className="spin" /> : <ArrowUp size={18} />}
          </button>
        </div>
        {videoBatch && <button type="button" className="secondary-button" disabled={busy} onClick={() => setBatchOpen(true)}><Clapperboard size={15} />查看视频批次</button>}
        {failure && <p className="starter-error" aria-live="polite">{failure}</p>}
      </div>
      {videoBatch && draftProject.current && <VideoBatchDialog open={batchOpen} projectID={draftProject.current.project_id} snapshot={videoBatch} onChange={continueWithVideoBatch} onPendingChange={setBatchPending} onClose={() => setBatchOpen(false)} />}
    </section>
  );
}

function NameDialog({ mode, initialValue, onClose, onSubmit }: { mode: "create" | "rename"; initialValue: string; onClose: () => void; onSubmit: (value: string) => Promise<void> }) {
  const [value, setValue] = useState(initialValue);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => input.current?.focus(), []);
  const submit = async () => {
    if (!value.trim() || busy) return;
    setBusy(true); setFailure("");
    try { await onSubmit(value.trim()); } catch (error) { setFailure(errorText(error)); setBusy(false); }
  };
  return <div className="modal-layer" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}><section className="dialog" role="dialog" aria-modal="true" aria-labelledby="name-dialog-title"><div className="dialog-head"><h2 id="name-dialog-title">{mode === "create" ? "新建作品" : "重命名作品"}</h2><button className="icon-button ghost" aria-label="关闭" onClick={onClose}><X size={17} /></button></div><div className="dialog-body"><label>作品名称<input ref={input} name="project-title" autoComplete="off" value={value} onChange={(event) => setValue(event.target.value)} onKeyDown={(event) => event.key === "Enter" && void submit()} maxLength={80} /></label>{failure && <p className="form-error" aria-live="polite">{failure}</p>}</div><div className="dialog-actions"><button className="secondary-button" onClick={onClose}>取消</button><button className="primary-button" disabled={!value.trim() || busy} onClick={() => void submit()}>{busy ? "正在保存…" : mode === "create" ? "创建并打开" : "保存名称"}</button></div></section></div>;
}

function DeleteDialog({ project, preview, onClose, onPreview, onConfirm }: { project: Project; preview: ProjectDeletePreview | null; onClose: () => void; onPreview: () => Promise<void>; onConfirm: () => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const run = async (action: () => Promise<void>) => { setBusy(true); setFailure(""); try { await action(); } catch (error) { setFailure(errorText(error)); } finally { setBusy(false); } };
  return <div className="modal-layer"><section className="dialog delete-dialog" role="alertdialog" aria-modal="true">
    <div className="dialog-head"><div><span className="danger-icon"><Trash2 size={18} /></span><h2>删除“{project.title}”</h2></div><button className="icon-button ghost" aria-label="关闭" onClick={onClose}><X size={17} /></button></div>
    <div className="dialog-body">{preview ? <>
      <p>删除后无法恢复该作品入口，源文件将进入删除流程；已生成记录保留用于审计。</p>
      <dl className="impact-list">
        <div><dt>来源文件</dt><dd>{preview.impact.asset_count} 个</dd></div>
        {(preview.impact.working_file_version_count ?? 0) > 0 && <div><dt>工作文件及历史版本</dt><dd>{preview.impact.working_file_version_count} 个，将删除</dd></div>}
        {(preview.impact.subtask_result_count ?? 0) > 0 && <div><dt>子任务结果正文</dt><dd>{preview.impact.subtask_result_count} 个，将删除</dd></div>}
        {(preview.impact.tool_reconciliation_count ?? 0) > 0 && <div><dt>外部操作核对依据</dt><dd>{preview.impact.tool_reconciliation_count} 个，将删除</dd></div>}
        {(preview.impact.instruction_version_count ?? 0) > 0 && <div><dt>作品规则及历史版本</dt><dd>{preview.impact.instruction_version_count} 个，将删除</dd></div>}
        {(preview.impact.instruction_snapshot_count ?? 0) > 0 && <div><dt>执行规则快照</dt><dd>{preview.impact.instruction_snapshot_count} 个，将删除</dd></div>}
        {(preview.impact.instruction_proposal_count ?? 0) > 0 && <div><dt>规则变更提案</dt><dd>{preview.impact.instruction_proposal_count} 个，将删除</dd></div>}
        <div><dt>已生成产物</dt><dd>{preview.impact.artifact_count} 个</dd></div>
        <div><dt>对话消息</dt><dd>{preview.impact.message_count} 条</dd></div>
        {preview.active_run_impacts.length > 0 && <div><dt>未结束任务</dt><dd>{preview.active_run_impacts.length} 个，将自动结束</dd></div>}
      </dl>
    </> : <p>系统会先计算影响范围，不会直接删除。</p>}{failure && <p className="form-error" aria-live="polite">{failure}</p>}</div>
    <div className="dialog-actions"><button className="secondary-button" onClick={onClose}>取消</button>{preview ? <button className="destructive-button" disabled={busy} onClick={() => void run(onConfirm)}>{busy ? "正在删除…" : "确认删除作品"}</button> : <button className="destructive-outline" disabled={busy} onClick={() => void run(onPreview)}>{busy ? "正在检查…" : "查看删除影响"}</button>}</div>
  </section></div>;
}

function Workbench({ projectID }: { projectID: string }) {
  const principal = usePrincipal();
  const refreshPrincipal = useContext(RefreshPrincipalContext);
  const messageCacheKey = pendingMessageKey(principal, projectID);
  const refreshSequence = useRef(0);
  const snapshotResetPending = useRef(false);
  const [mobilePane, setMobilePane] = useState<"artifacts" | "workspace" | "agent">("agent");
  const [project, setProject] = useState<Project | null>(null);
  const [projectGoal, setProjectGoal] = useState<unknown>(null);
  const [messages, setMessages] = useState<Message[]>([]);
  const [agentTurns, setAgentTurns] = useState<AgentTurn[]>([]);
  const [agentTasks, setAgentTasks] = useState<AgentTask[]>([]);
  const [agentToolCalls, setAgentToolCalls] = useState<AgentToolCall[]>([]);
  const [agentTurnLive, setAgentTurnLive] = useState<Record<string, AgentTurnLiveState>>({});
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [projectAssets, setProjectAssets] = useState<Asset[]>([]);
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [capabilities, setCapabilities] = useState<ComposerRegistryEntry[]>([]);
  const [approvalRegistry, setApprovalRegistry] = useState<WorkspaceRegistries["approvals"]>([]);
  const [taskRegistry, setTaskRegistry] = useState<WorkspaceRegistries["tasks"]>([]);
  const [interactionRegistry, setInteractionRegistry] = useState<WorkspaceRegistries["interactions"]>([]);
  const [artifactPresentations, setArtifactPresentations] = useState<ArtifactPresentation[]>([]);
  const [candidates, setCandidates] = useState<ScriptCandidate[]>([]);
  const [finalSelection, setFinalSelection] = useState<FinalSelection | null>(null);
  const [runSnapshot, setRunSnapshot] = useState<RunSnapshot | null>(null);
  const [proposedActions, setProposedActions] = useState<ProposedAction[]>([]);
  const [activities, setActivities] = useState<ProjectActivity[]>([]);
  const [revisionRequests, setRevisionRequests] = useState<RevisionRequest[]>([]);
  const [targetResolutions, setTargetResolutions] = useState<TargetResolution[]>([]);
  const [previewRevision, setPreviewRevision] = useState<RevisionRequest | null>(null);
  const [streamState, setStreamState] = useState<"connecting" | "connected" | "reconnecting">("connecting");
  const [selectedArtifact, setSelectedArtifact] = useState<string | null>(null);
  const [artifactVersion, setArtifactVersion] = useState<ArtifactVersion | null>(null);
  const [candidatePreviewVersion, setCandidatePreviewVersion] = useState<ArtifactVersion | null>(null);
  const [displayedArtifactVersion, setDisplayedArtifactVersion] = useState<ArtifactVersion | null>(null);
  const [artifactFailure, setArtifactFailure] = useState("");
  const [editRequestedArtifactID, setEditRequestedArtifactID] = useState<string | null>(null);
  const [artifactDirty, setArtifactDirty] = useState(false);
  const [targetSelection, setTargetSelection] = useState<AgentTargetSelection | null>(null);
  const [failure, setFailure] = useState("");
  const [loading, setLoading] = useState(true);
  const proposalRequestKeys = useRef(new Map<string, { fingerprint: string; key: string }>());
  const approvalRequestKeys = useRef(new Map<string, { fingerprint: string; key: string }>());
  const revisionRequestKeys = useRef(new Map<string, { fingerprint: string; key: string }>());
  const revisionPendingActions = useRef(new Map<string, { subject: string; action: string }>());
  const scriptEditRequestKeys = useRef(new Map<string, { fingerprint: string; key: string }>());
  const runActionRequestKeys = useRef(new Map<string, Map<string, string>>());
  const runActionSubmitting = useRef(new Set<string>());
  const taskControlRequestKeys = useRef(new Map<string, Map<string, string>>());
  const taskControlSubmitting = useRef(new Set<string>());
  const proposalSubmitting = useRef(new Set<string>());
  const toolApprovalRequestKeys = useRef(new Map<string, { fingerprint: string; key: string }>());
  const toolApprovalPendingActions = useRef(new Map<string, { subject: string; action: "approve" | "reject" }>());
  const turnCommandKeys = useRef(new Map<string, { fingerprint: string; key: string }>());
  const turnCommandsSubmitting = useRef(new Set<string>());
  const toolApprovalSubmitting = useRef(new Set<string>());
  const revisionSubmitting = useRef(new Set<string>());
  const scriptEditSubmitting = useRef(new Set<string>());

  const load = async (showLoading = false, requireSuccess = false) => {
    const sequence = ++refreshSequence.current;
    const resetSnapshot = snapshotResetPending.current;
    if (showLoading) setLoading(true);
    setFailure("");
    try {
      const [projection, assets] = await Promise.all([api.getProjectWorkspaceProjection(projectID), api.listAssets(projectID)]);
      if (sequence !== refreshSequence.current) {
        if (requireSuccess) throw new ApiError("WORKSPACE_REFRESH_SUPERSEDED", "能力目录刷新被更新的读取替代，请重试刷新。", 409);
        return;
      }
      const snapshot = projection.snapshot;
      const snapshotTurns = snapshot.agent_turns ?? [];
      setAgentTurns(snapshotTurns);
      setAgentTasks(snapshot.agent_tasks ?? []);
      setAgentToolCalls((current) => (snapshot.agent_tool_calls ?? []).map((incoming) => {
        if (resetSnapshot) return incoming;
        const known = current.find((call) => call.agent_tool_call_id === incoming.agent_tool_call_id);
        if (known?.approval && incoming.approval && known.approval.agent_tool_approval_id === incoming.approval.agent_tool_approval_id && known.approval.version > incoming.approval.version) return { ...incoming, approval: known.approval, approval_status: known.approval_status, status: incoming.status === "pending_approval" ? known.status : incoming.status };
        return incoming;
      }));
      setProjectGoal(snapshot.goal ?? null);
      setProject(snapshot.project); setMessages((current) => {
        let pending = pendingMessageCache.get(messageCacheKey);
        if (resetSnapshot && pending) {
          pending = { ...pending, delivery_status: "unconfirmed" };
          pendingMessageCache.set(messageCacheKey, pending);
        }
        const local = pending ? [...current, pending] : current;
        const retained = resetSnapshot ? local.map((message) => message.delivery_status ? { ...message, delivery_status: "unconfirmed" as const } : message) : local;
        const merged = mergeSnapshotMessages(snapshot.messages, retained);
        const reconciled = mergeMessagesWithAgentTurns(merged, snapshotTurns);
        if (pending && !reconciled.some((message) => message.message_id === pending.message_id)) pendingMessageCache.delete(messageCacheKey);
        return reconciled;
      }); setArtifacts(snapshot.artifacts); setApprovals(snapshot.approvals); setCapabilities(projection.registries.composer); setApprovalRegistry(projection.registries.approvals); setTaskRegistry(projection.registries.tasks); setInteractionRegistry(projection.registries.interactions); setArtifactPresentations(artifactPresentationsFromRegistries(projection.registries)); setCandidates(snapshot.script_candidates); setFinalSelection(snapshot.final_selection);
      setProjectAssets(assets);
      setRunSnapshot(snapshot.active_run ?? snapshot.latest_run ?? null);
      setProposedActions((current) => {
        const snapshotActions = snapshot.proposed_actions ?? [
          ...(resetSnapshot ? [] : current.filter((action) => action.status === "consumed")),
          ...snapshot.pending_proposed_actions,
        ];
        const latestPendingByCapability = new Map<string, string>();
        snapshotActions.forEach((action) => {
          if (action.status === "pending") {
            latestPendingByCapability.set(action.capability_ref.capability_id, action.proposed_action_id);
          }
        });
        return snapshotActions.map((action) => {
          const known = current.find((item) => item.proposed_action_id === action.proposed_action_id);
          return !resetSnapshot && known && (known.version > action.version || known.version === action.version && known.status === "consumed" && action.status === "pending") ? known : action;
        }).filter((action) =>
          action.status !== "pending"
          || latestPendingByCapability.get(action.capability_ref.capability_id) === action.proposed_action_id
        );
      });
      setActivities(snapshot.activities ?? []); setRevisionRequests((current) => (snapshot.revision_requests ?? []).map((incoming) => {
        const known = current.find((item) => item.revision_request_id === incoming.revision_request_id);
        return !resetSnapshot && known && known.version > incoming.version ? known : incoming;
      })); setTargetResolutions(snapshot.target_resolutions ?? []);
      const projectedPresentations = artifactPresentationsFromRegistries(projection.registries);
      const visibleArtifacts = snapshot.artifacts.filter((item) => artifactIsVisible(projectedPresentations, item));
      const fallbackArtifactID = visibleArtifacts.find((item) => item.current_version_id === snapshot.project.current_focus_artifact_version_id)?.artifact_id
        ?? [...visibleArtifacts].sort((left, right) => compareArtifacts(projectedPresentations, left, right)).at(-1)?.artifact_id
        ?? null;
      setSelectedArtifact((current) => visibleArtifacts.some((item) => item.artifact_id === current) ? current : fallbackArtifactID);
      snapshotResetPending.current = false;
    } catch (error) {
      if (sequence === refreshSequence.current) {
        setFailure(errorText(error));
        if (error instanceof ApiError && [401, 403, 404].includes(error.status)) setProject(null);
      }
      if (requireSuccess) throw error;
    } finally {
      if (sequence === refreshSequence.current) setLoading(false);
    }
  };
  useEffect(() => { void load(true); return () => { ++refreshSequence.current; }; }, [projectID]);
  useEffect(() => {
    if (!previewRevision) return;
    const current = revisionRequests.find((item) => item.revision_request_id === previewRevision.revision_request_id);
    if (current && (current.status !== "proposed" || current.version !== previewRevision.version)) setPreviewRevision(null);
  }, [revisionRequests, previewRevision]);

  useEffect(() => {
    const source = new EventSource(`/api/v1/projects/${projectID}/events/stream`);
    let closed = false;
    const closeSource = () => { if (!closed) { closed = true; source.close(); } };
    let refreshTimer: number | undefined;
    const refresh = () => { if (closed) return; window.clearTimeout(refreshTimer); refreshTimer = window.setTimeout(() => void load(), 120); };
    const eventTypes = ["project.renamed", "message.created", "run.started", "run.running", "run.paused", "run.resumed", "run.failed", "run.completed", "run.cancelled", "run.waiting_approval", "step.started", "step.completed", "step.failed", "task.queued", "task.started", "task.progressed", "task.completed", "task.failed", "agent_task.queued", "agent_task.started", "agent_task.progressed", "agent_task.completed", "agent_task.failed", "agent_task.cancelled", "agent_task.retry_scheduled", "agent.tool_approval.requested", "agent.tool_approval.approved", "agent.tool_approval.rejected", "agent.tool_call.started", "agent.tool_call.completed", "agent.tool_call.failed", "agent.tool_call.cancelled", "approval.requested", "approval.resolved", "approval.expired", "artifact.created", "artifact.version_created", "artifact.confirmed", "artifact.invalidated", "asset_set.created", "asset_set.version_created", "asset_set.member_added", "asset_set.member_removed", "asset_set.order_changed", "asset_set.completeness_checked", "asset_set.sealed", "asset_set.reopened", "asset_set.superseded", "quality_review.started", "quality_review.batch_completed", "quality_review.passed", "quality_review.action_required", "quality_review.override_confirmed", "quality_review.failed", "script_candidate.created", "final_selection.requested", "final_selection.changed", "target_resolution.created", "target_resolution.resolved", "revision_request.created", "revision_request.running", "revision_request.proposed", "revision_request.failed", "revision_request.accepted", "revision_request.rejected", "revision_request.cancelled", "stream.reset_required"];
    eventTypes.push("agent_task.input_received", "agent.turn.queued_updated", "agent_task.waiting_approval", "agent_task.resumed", "agent_task.pause_requested", "agent_task.paused", "agent_task.resume_requested", "task.waiting_approval", "task.resumed", "task.paused");
    eventTypes.push("execution.input_received", "execution.inputs_included", "execution.input_changed", "agent_task.input_changed");
    eventTypes.push("proposed_action.created", "proposed_action.configured", "proposed_action.input_bound", "proposed_action.consumed");
    eventTypes.push("script_edit.awaiting_handoff_refresh", "script_edit.completed", "script_handoff.refresh_requested");
    eventTypes.push("revision_request.queued", "revision_request.waiting_safe_checkpoint", "revision_request.no_change", "revision_request.stale");
    const refreshTypes = eventTypes.filter((type) => type !== "stream.reset_required");
    refreshTypes.forEach((type) => source.addEventListener(type, refresh));
    const streamControl = (browserEvent: Event) => {
      let payload: unknown;
      try { payload = JSON.parse((browserEvent as MessageEvent<string>).data); } catch { return; }
      if (closed || !payload || typeof payload !== "object" || !("stream_scope" in payload) || payload.stream_scope !== "project") return;
      if (browserEvent.type === "stream.snapshot_invalidated") {
        if (!("project_id" in payload) || payload.project_id !== projectID || !("project_event_seq" in payload) || !Number.isSafeInteger(payload.project_event_seq) || Number(payload.project_event_seq) < 1) return;
        refresh();
      } else if (browserEvent.type === "stream.reset_required") {
        if (!("requires_snapshot" in payload) || payload.requires_snapshot !== true) return;
        ++refreshSequence.current;
        snapshotResetPending.current = true;
        setStreamState("reconnecting");
        refresh();
      } else if (browserEvent.type === "stream.access_revoked") {
        window.clearTimeout(refreshTimer);
        ++refreshSequence.current;
        closeSource();
        setProject(null);
        setLoading(false);
        setFailure("当前登录或作品访问权限已失效，请重新登录或刷新。");
        void refreshPrincipal(true);
      } else {
        setStreamState("reconnecting");
        refresh();
      }
    };
    const controlTypes = ["stream.snapshot_invalidated", "stream.access_revoked", "stream.error", "stream.reset_required"];
    controlTypes.forEach((type) => source.addEventListener(type, streamControl));
    const agentEventTypes = ["agent.turn.started", "agent.updated", "agent.tool.started", "agent.tool.completed", "agent.output.delta", "agent.approval.requested", "agent.artifact.created", "agent.turn.waiting_approval", "agent.turn.committed", "agent.turn.failed", "agent.turn.cancelled"];
    agentEventTypes.push("agent.turn.pause_requested", "agent.turn.paused", "agent.turn.resume_requested", "agent.turn.input_received", "agent.turn.inputs_included", "agent.turn.input_changed");
    const handleAgentEvent = (browserEvent: Event) => {
      if (closed) return;
      const raw = browserEvent as MessageEvent<string>;
      let parsed: unknown;
      try { parsed = JSON.parse(raw.data); } catch { return; }
      const event = parseAgentEvent(parsed);
      if (!event || event.project_id !== projectID) return;
      setAgentTurnLive((current) => ({ ...current, [event.turn_id]: nextAgentTurnLiveState(current[event.turn_id], event) }));
      const statusByEvent: Partial<Record<AgentEventEnvelope["event_type"], AgentTurn["status"]>> = {
        "agent.turn.started": "running",
        "agent.turn.waiting_approval": "waiting_approval",
        "agent.turn.pause_requested": "pausing",
        "agent.turn.paused": "paused",
        "agent.turn.resume_requested": event.payload.status === "waiting_approval" ? "waiting_approval" : "accepted",
        "agent.turn.committed": "committed",
        "agent.turn.failed": "failed",
        "agent.turn.cancelled": "cancelled",
      };
      const status = statusByEvent[event.event_type];
      if (status) setAgentTurns((items) => items.map((turn) => turn.agent_turn_id === event.turn_id ? { ...turn, status } : turn));
      if (agentEventIsTerminal(event) || event.event_type === "agent.turn.waiting_approval" || event.event_type === "agent.approval.requested" || event.event_type === "agent.artifact.created" || ["agent.turn.pause_requested", "agent.turn.paused", "agent.turn.resume_requested", "agent.turn.input_received", "agent.turn.inputs_included", "agent.turn.input_changed"].includes(event.event_type)) refresh();
    };
    agentEventTypes.forEach((type) => source.addEventListener(type, handleAgentEvent));
    source.onopen = () => {
      if (closed) return;
      setStreamState("connected");
      void load();
    };
    source.onerror = () => { if (!closed) setStreamState("reconnecting"); };
    return () => {
      window.clearTimeout(refreshTimer);
      refreshTypes.forEach((type) => source.removeEventListener(type, refresh));
      controlTypes.forEach((type) => source.removeEventListener(type, streamControl));
      agentEventTypes.forEach((type) => source.removeEventListener(type, handleAgentEvent));
      closeSource();
    };
  }, [projectID, refreshPrincipal]);

  const projectLoaded = Boolean(project);
  const needsFallbackRefresh = projectLoaded && (
    agentTurns.some((turn) => ["accepted", "running", "waiting_approval", "pausing", "cancel_requested", "committing"].includes(turn.status))
    || agentTasks.some((task) => ["queued", "running", "waiting_approval", "pausing"].includes(task.status))
    || Boolean(runSnapshot && ["pending", "running", "pausing", "waiting_approval"].includes(runSnapshot.run.status))
    || revisionRequests.some((revision) => ["queued", "waiting_safe_checkpoint", "running"].includes(revision.status))
  );
  useEffect(() => {
    if (streamState === "connected" || !projectLoaded) return;
    let polling = false;
    const pollProject = async () => {
      if (polling) return;
      polling = true;
      try {
        // Restore approvals, inputs and results together for every SDK mode.
        await load();
      } finally {
        polling = false;
      }
    };
    const timer = window.setInterval(() => void pollProject(), needsFallbackRefresh ? 3_000 : 15_000);
    return () => window.clearInterval(timer);
  }, [projectID, projectLoaded, needsFallbackRefresh, streamState]);

  useEffect(() => {
    const refreshVisibleState = () => {
      if (document.visibilityState === "visible") void load();
    };
    window.addEventListener("focus", refreshVisibleState);
    document.addEventListener("visibilitychange", refreshVisibleState);
    return () => {
      window.removeEventListener("focus", refreshVisibleState);
      document.removeEventListener("visibilitychange", refreshVisibleState);
    };
  }, [projectID]);

  const selectedArtifactVersionID = candidatePreviewVersion?.artifact_id === selectedArtifact ? candidatePreviewVersion.artifact_version_id : artifacts.find((item) => item.artifact_id === selectedArtifact)?.current_version_id ?? null;
  useEffect(() => {
    setArtifactVersion(null);
    setArtifactFailure("");
    if (!selectedArtifactVersionID) return;
    if (candidatePreviewVersion?.artifact_id === selectedArtifact && candidatePreviewVersion.artifact_version_id === selectedArtifactVersionID) { setArtifactVersion(candidatePreviewVersion); return; }
    let active = true;
    void api.getArtifactVersion(selectedArtifactVersionID).then((version) => active && setArtifactVersion(version)).catch((error) => active && setArtifactFailure(errorText(error)));
    return () => { active = false; };
  }, [selectedArtifactVersionID, candidatePreviewVersion]);

  useEffect(() => { setTargetSelection(null); }, [selectedArtifact, displayedArtifactVersion?.artifact_version_id, artifactDirty]);

  if (loading) return <div className="app-loading">正在打开作品…</div>;
  if (!project) return <div className="app-loading"><ConnectionFailure message={failure || "作品不存在。"} retry={load} /><a className="secondary-button" href="/"><ArrowLeft size={15} />返回作品</a></div>;
	const currentArtifactVersion = artifactVersion?.artifact_id === selectedArtifact ? artifactVersion : null;
	const activeArtifact = artifacts.find((item) => item.artifact_id === selectedArtifact && artifactIsVisible(artifactPresentations, item)) ?? null;
	// Explicit candidate navigation may go backwards; ordinary refreshes must not.
	const artifactWorkspaceKey = candidatePreviewVersion && candidatePreviewVersion.artifact_id === activeArtifact?.artifact_id
		? `${candidatePreviewVersion.artifact_id}:candidate:${candidatePreviewVersion.artifact_version_id}`
		: activeArtifact?.artifact_id ?? "empty";
	const viewedCapabilityID = resolveViewedCapabilityID(
		activeArtifact?.run_id,
		activities,
		project.active_write_run_id,
		project.current_capability_id,
	);
  const composerContext: AgentComposerContext = {
    view: {
      project_id: project.project_id,
      run_id: activeArtifact?.run_id ?? null,
      capability_id: viewedCapabilityID ?? null,
      artifact_id: activeArtifact?.artifact_id ?? null,
      artifact_version_id: !artifactDirty && !previewRevision && !artifactFailure && displayedArtifactVersion?.artifact_id === selectedArtifact ? displayedArtifactVersion.artifact_version_id : null,
      artifact_type: activeArtifact?.artifact_type ?? null,
      scope_key: scopeNumber(activeArtifact?.scope_key) > 0 ? activeArtifact?.scope_key ?? null : null,
      artifact_label: activeArtifact ? presentationFor(artifactPresentations, activeArtifact.artifact_type).label : null,
    },
    selection: artifactDirty || previewRevision || artifactFailure ? null : targetSelection,
  };
  const selectArtifact = (artifactID: string) => {
    if (artifactID === selectedArtifact && !candidatePreviewVersion) return;
    if (artifactDirty && !window.confirm("当前产物有未保存修改。放弃修改并切换吗？")) return;
    setCandidatePreviewVersion(null);
    setArtifactDirty(false);
    setSelectedArtifact(artifactID);
    setPreviewRevision(null);
  };
  const proposalRequestKey = (operation: string, action: ProposedAction, payload: unknown) => {
    const slot = `${operation}:${action.proposed_action_id}`;
    const fingerprint = canonicalJSONStringify({ project_id: projectID, proposed_action_id: action.proposed_action_id, version: action.version, snapshot_hash: action.snapshot_hash, capability_ref: action.capability_ref, input: action.input, config: action.config, confirmation_message_id: action.confirmation_message_id, payload });
    const previous = proposalRequestKeys.current.get(slot);
    if (previous?.fingerprint === fingerprint) return previous.key;
    const key = createUUID();
    proposalRequestKeys.current.set(slot, { fingerprint, key });
    return key;
  };
  const rememberProposedAction = (action: ProposedAction) => {
    ++refreshSequence.current;
    setProposedActions((items) => {
      const current = items.find((item) => item.proposed_action_id === action.proposed_action_id);
      if (!current) return [...items, action];
      if (current.status !== "pending" || current.version > action.version) return items;
      return items.map((item) => item.proposed_action_id === action.proposed_action_id ? action : item);
    });
  };
  const send: SendMessage = async (content, capability, attachments, videoBatch, signal, idempotencyKey, binding) => {
    if (binding && binding.context.view.project_id !== projectID) throw new ApiError("RESOURCE_PROJECT_MISMATCH", "原消息上下文不属于当前作品，未发送。", 409);
    const submittedContext: AgentComposerContext = binding?.context ?? (videoBatch ? {
      ...composerContext,
      view: { ...composerContext.view, asset_set_version_id: videoBatch.version.asset_set_version_id },
    } : composerContext);
    const submittedSelection = submittedContext.selection;
    const requestKey = idempotencyKey ?? createUUID();
    const submissionID = await agentTurnSubmissionID(principal.user_id, project.primary_conversation_id, requestKey);
    if (signal.aborted) return;
    const cachedPending = pendingMessageCache.get(messageCacheKey);
    const pending = {
      ...(cachedPending?.submissionKey === requestKey ? cachedPending : createOptimisticUserMessage(content, submittedContext)),
      delivery_status: "sending" as const, submissionKey: requestKey, submission_id: submissionID, transportID: createUUID(),
    };
    const ownsPending = () => pendingMessageCache.get(messageCacheKey)?.transportID === pending.transportID;
    pending.message_context = { ...pending.message_context!, attachment_refs: attachments };
    pendingMessageCache.set(messageCacheKey, pending);
    setMessages((items) => [...items.filter((item) => item.message_id !== pending.message_id), pending]);
    if (submittedSelection) setTargetSelection(null);
    const markUnconfirmed = () => {
      if (ownsPending()) {
        setMessages((items) => markOptimisticMessageUnconfirmed(items, pending.message_id));
        pendingMessageCache.set(messageCacheKey, { ...pending, delivery_status: "unconfirmed" });
        if (submittedSelection) setTargetSelection((current) => current ?? submittedSelection);
      }
      void load();
    };
    let response: AgentTurn;
    try {
      response = await api.sendMessage(project.primary_conversation_id, content, capability, attachments, submittedContext, signal, requestKey, binding?.clientInstanceID);
      if (signal.aborted) { markUnconfirmed(); return; }
      if (!agentTurnReceiptMatches(response, { workspace_id: principal.workspace_id, user_id: principal.user_id, project_id: projectID, conversation_id: project.primary_conversation_id, submission_id: submissionID })) throw new ApiError("AGENT_TURN_RECEIPT_INVALID", "消息回执与原提交不匹配，结果仍待确认。", 502);
    } catch (error) {
      if (signal.aborted || (error instanceof DOMException && error.name === "AbortError")) {
        markUnconfirmed();
        return;
      }
      if (ownsPending()) {
        const definitive = !binding?.recovering && error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS";
        setMessages((items) => definitive ? markOptimisticMessageFailed(items, pending.message_id) : markOptimisticMessageUnconfirmed(items, pending.message_id));
        pendingMessageCache.set(messageCacheKey, { ...pending, delivery_status: definitive ? "failed" : "unconfirmed" });
        if (submittedSelection) setTargetSelection((current) => current ?? submittedSelection);
        if (!definitive) void load();
      }
      throw error;
    }
    if (!ownsPending()) return;
    if (pendingMessageCache.get(messageCacheKey)?.submissionKey === requestKey) pendingMessageCache.delete(messageCacheKey);
    setAgentTurns((items) => {
      const current = items.find((item) => item.agent_turn_id === response.agent_turn_id);
      if (current && Date.parse(current.updated_at) >= Date.parse(response.updated_at)) return items;
      return [...items.filter((item) => item.agent_turn_id !== response.agent_turn_id), response];
    });
    setMessages((items) => settleOptimisticMessage(items, pending.message_id, [agentTurnMessage(response)]));
    // A recovered terminal receipt may arrive after its SSE event was missed.
    if (["committed", "failed", "cancelled"].includes(response.status)) void load();
  };
  const executeTurnCommand = async (turn: AgentTurn, action: "edit" | "pause" | "resume" | "cancel", content?: string, suppliedKey?: string) => {
    const current = agentTurns.find((item) => item.agent_turn_id === turn.agent_turn_id);
    const states = { edit: ["accepted"], pause: ["accepted", "running", "waiting_approval", "pausing", "paused"], resume: ["paused"], cancel: ["accepted", "running", "waiting_approval", "pausing", "paused", "cancel_requested"] };
    if (principal.role === "viewer" || turn.project_id !== projectID || turn.workspace_id !== principal.workspace_id || !current || current.project_id !== projectID || current.conversation_id !== turn.conversation_id || current.user_id !== turn.user_id || (action !== "cancel" && current.user_id !== principal.user_id) || !states[action].includes(current.status) || (action === "edit" && (current.started_at || current.request.content !== turn.request.content))) throw new ApiError("AGENT_TURN_STATE_CONFLICT", "消息或操作权限已变化，请刷新后核对。", 409);
    if (turnCommandsSubmitting.current.has(turn.agent_turn_id)) throw new ApiError("COMMAND_IN_PROGRESS", "该消息的操作正在提交。", 409);
    const slot = `${turn.agent_turn_id}:${action}`;
    const fingerprint = canonicalJSONStringify(action === "edit" ? [projectID, turn.agent_turn_id, turn.request.content, content] : [projectID, turn.agent_turn_id, action, turn.status, turn.updated_at]);
    if (turnCommandKeys.current.get(slot)?.fingerprint !== fingerprint) turnCommandKeys.current.set(slot, { fingerprint, key: suppliedKey ?? createUUID() });
    const key = turnCommandKeys.current.get(slot)!.key;
    turnCommandsSubmitting.current.add(turn.agent_turn_id);
    ++refreshSequence.current;
    try {
      const updated = action === "edit" ? await api.updateQueuedAgentTurn(turn, content!, key) : action === "cancel" ? (await api.cancelAgentTurn(turn.agent_turn_id, key)).turn : await api.controlAgentTurnPause(turn.agent_turn_id, action, key);
      if (!updated || updated.agent_turn_id !== turn.agent_turn_id || updated.project_id !== projectID || updated.workspace_id !== turn.workspace_id || updated.conversation_id !== turn.conversation_id || updated.user_id !== turn.user_id) throw new ApiError("AGENT_TURN_RECEIPT_INVALID", "消息操作回执不属于当前执行，请刷新确认。", 502);
      turnCommandKeys.current.delete(slot);
      // Control receipts may be older than a concurrent worker/SSE update.
      // Reload the authoritative projection instead of replacing its rows.
    } catch (error) {
      if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") turnCommandKeys.current.delete(slot);
      throw error;
    } finally {
      try { await load(); } finally { turnCommandsSubmitting.current.delete(turn.agent_turn_id); }
    }
  };
  const editQueuedAgentTurn = (turn: AgentTurn, content: string) => executeTurnCommand(turn, "edit", content);
  const controlAgentTurnPause = (turn: AgentTurn, action: "pause" | "resume", key: string) => executeTurnCommand(turn, action, undefined, key);

  const appendAgentTurnInput = async (turn: AgentTurn, content: string, key: string, refs?: AttachmentRef[]) => {
    ++refreshSequence.current;
    try {
      const input = refs?.length ? await api.appendAgentTurnInput(turn.agent_turn_id, content, key, refs) : await api.appendAgentTurnInput(turn.agent_turn_id, content, key);
      setAgentTurns((items) => items.map((item) => item.agent_turn_id === turn.agent_turn_id ? { ...item, additional_inputs: [...(item.additional_inputs ?? []).filter((entry) => entry.input_id !== input.input_id), input].sort((left, right) => left.sequence - right.sequence) } : item));
    } finally { void load(); }
  };
	const cancelAgentTurn = (turn: AgentTurn) => executeTurnCommand(turn, "cancel");
  const beginRevisionSubmission = (revision: RevisionRequest, action: string) => {
    const current = revisionRequests.find((item) => item.revision_request_id === revision.revision_request_id);
    if (!current || current.project_id !== projectID || current.version !== revision.version || current.status !== revision.status || current.conversation_id !== revision.conversation_id || current.request_message_id !== revision.request_message_id || current.target_resolution_id !== revision.target_resolution_id || current.artifact_id !== revision.artifact_id || current.base_artifact_version_id !== revision.base_artifact_version_id) throw new ApiError("REVISION_STATE_CONFLICT", "修改请求已变化，请刷新核对。", 409);
    if (revisionSubmitting.current.has(revision.revision_request_id)) throw new ApiError("COMMAND_IN_PROGRESS", "该修改请求正在提交。", 409);
    const subject = canonicalJSONStringify([projectID, revision.conversation_id, revision.revision_request_id, revision.version, revision.status, revision.target_resolution_id, revision.artifact_id, revision.base_artifact_version_id]);
    const pending = revisionPendingActions.current.get(revision.revision_request_id);
    if (pending?.subject === subject && pending.action !== action) throw new ApiError("REVISION_OUTCOME_UNKNOWN", "前一次返修操作结果尚未确认，请先重试原操作或刷新确认。", 409);
    revisionPendingActions.current.set(revision.revision_request_id, { subject, action });
    revisionSubmitting.current.add(revision.revision_request_id);
  };
  const startRevision = async (revision: RevisionRequest, resolution?: TargetResolution, candidateID?: string) => {
    const current = revisionRequests.find((item) => item.revision_request_id === revision.revision_request_id);
    const target = resolution ? targetResolutions.find((item) => item.target_resolution_id === resolution.target_resolution_id) : undefined;
    const candidate = target?.candidates.find((item) => item.candidate_id === candidateID);
    const shownCandidate = resolution?.candidates.find((item) => item.candidate_id === candidateID);
    if (resolution && (!candidate || candidate.artifact_id !== shownCandidate?.artifact_id || candidate.artifact_version_id !== shownCandidate?.artifact_version_id)) throw new ApiError("REVISION_STATE_CONFLICT", "定位候选已变化，请刷新核对。", 409);
    if (principal.role === "viewer" || revision.project_id !== projectID || !current || current.version !== revision.version || current.status !== revision.status || current.target_resolution_id !== revision.target_resolution_id || (resolution ? !target || target.project_id !== projectID || target.conversation_id !== revision.conversation_id || target.request_message_id !== revision.request_message_id || target.status !== "ambiguous" || revision.status !== "waiting_target_confirmation" || target.target_resolution_id !== revision.target_resolution_id || !candidate : !["queued", "failed"].includes(revision.status))) throw new ApiError("REVISION_STATE_CONFLICT", "修改请求或定位候选已变化，请刷新核对。", 409);
    const slot = `${revision.revision_request_id}:${resolution ? `target:${candidateID}` : "execute"}`;
    const fingerprint = canonicalJSONStringify([projectID, revision.conversation_id, revision.version, revision.target_resolution_id, candidate?.artifact_id ?? revision.artifact_id, candidate?.artifact_version_id ?? revision.base_artifact_version_id]);
    if (revisionRequestKeys.current.get(slot)?.fingerprint !== fingerprint) revisionRequestKeys.current.set(slot, { fingerprint, key: createUUID() });
    const key = revisionRequestKeys.current.get(slot)!.key;
    beginRevisionSubmission(revision, slot);
    ++refreshSequence.current;
    try {
      const updated = resolution ? await api.resolveRevisionTarget(resolution.target_resolution_id, candidateID!, key, revision.version) : await api.executeRevision(revision.revision_request_id, key, revision.version);
      if (!updated || updated.revision_request_id !== revision.revision_request_id || updated.project_id !== projectID || updated.conversation_id !== revision.conversation_id || updated.request_message_id !== revision.request_message_id || updated.target_resolution_id !== revision.target_resolution_id || updated.version < revision.version + 1 || updated.artifact_id !== (candidate?.artifact_id ?? revision.artifact_id) || updated.base_artifact_version_id !== (candidate?.artifact_version_id ?? revision.base_artifact_version_id)) throw new ApiError("REVISION_RECEIPT_INVALID", "返修操作回执不属于当前请求或目标，请刷新确认。", 502);
      ++refreshSequence.current;
      setRevisionRequests((items) => items.map((item) => item.revision_request_id === revision.revision_request_id && item.version <= updated.version ? updated : item));
      revisionPendingActions.current.delete(revision.revision_request_id);
    } catch (error) {
      if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") {
        revisionRequestKeys.current.delete(slot);
        revisionPendingActions.current.delete(revision.revision_request_id);
      }
      throw error;
    } finally {
      try { await load(); } finally { revisionSubmitting.current.delete(revision.revision_request_id); }
    }
  };
  const resolveRevisionTarget = async (resolution: TargetResolution, candidateID: string) => {
    const revision = revisionRequests.find((item) => item.target_resolution_id === resolution.target_resolution_id);
    if (!revision) throw new ApiError("REVISION_STATE_CONFLICT", "定位结果没有匹配的修改请求。", 409);
    await startRevision(revision, resolution, candidateID);
  };
  const executeRevision = (revision: RevisionRequest) => startRevision(revision);
  const finishRevision = async (stage: "accept" | "reject" | "cancel", revision: RevisionRequest) => {
    const allowed = stage === "accept" || stage === "reject" ? ["proposed"] : ["waiting_target_confirmation", "waiting_safe_checkpoint", "queued", "failed"];
    if (principal.role === "viewer" || revision.project_id !== projectID || !allowed.includes(revision.status)) throw new ApiError("REVISION_STATE_CONFLICT", "当前修改请求不可提交，请刷新。", 409);
    const slot = `${revision.revision_request_id}:${stage}`;
    const fingerprint = canonicalJSONStringify({ project_id: projectID, conversation_id: revision.conversation_id, revision_id: revision.revision_request_id, version: revision.version, base: revision.base_artifact_version_id, artifact: revision.artifact_id });
    if (revisionRequestKeys.current.get(slot)?.fingerprint !== fingerprint) revisionRequestKeys.current.set(slot, { fingerprint, key: createUUID() });
    beginRevisionSubmission(revision, slot);
    ++refreshSequence.current;
    try {
      const key = revisionRequestKeys.current.get(slot)!.key;
      let updated: RevisionRequest;
      if (stage === "accept") {
        const result = await api.acceptRevision(revision, key);
        updated = result.revision_request;
        if (result.version_result?.artifact_version?.artifact_id !== revision.artifact_id || !result.version_result.artifact_version.artifact_version_id) throw new ApiError("REVISION_RECEIPT_INVALID", "接受回执与当前产物不一致，请刷新确认。", 502);
      } else updated = stage === "reject" ? await api.rejectRevision(revision, key) : await api.cancelRevision(revision, key);
      const status = stage === "accept" ? "accepted" : stage === "reject" ? "rejected" : "cancelled";
      if (!updated || updated.revision_request_id !== revision.revision_request_id || updated.project_id !== projectID || updated.conversation_id !== revision.conversation_id || updated.version !== revision.version + 1 || updated.status !== status) throw new ApiError("REVISION_RECEIPT_INVALID", "操作回执与当前修改请求不一致，请刷新确认。", 502);
      setRevisionRequests((items) => items.map((item) => item.revision_request_id === revision.revision_request_id && item.version <= revision.version ? updated : item));
      setPreviewRevision((current) => current?.revision_request_id === revision.revision_request_id ? null : current);
      revisionPendingActions.current.delete(revision.revision_request_id);
    } catch (error) {
      if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") {
        revisionRequestKeys.current.delete(slot);
        revisionPendingActions.current.delete(revision.revision_request_id);
      }
      throw error;
    } finally {
      try { await load(); } finally { revisionSubmitting.current.delete(revision.revision_request_id); }
    }
  };
  const acceptRevision = (revision: RevisionRequest) => finishRevision("accept", revision);
  const rejectRevision = (revision: RevisionRequest) => finishRevision("reject", revision);
  const cancelRevision = (revision: RevisionRequest) => finishRevision("cancel", revision);
	const previewRevisionDraft = (revision: RevisionRequest) => { if (!revision.artifact_id || !revision.proposal_payload) return; setSelectedArtifact(revision.artifact_id); setPreviewRevision(revision); };
  const resolveApproval = async (approval: Approval, action: string, instruction = "", resolutionPayload?: unknown) => {
    if (principal.role === "viewer" || approval.status !== "pending") throw new ApiError("APPROVAL_ACTION_NOT_ALLOWED", "当前确认不可提交。", 403);
    const requestKey = (stage: string, payload?: unknown) => {
      const slot = `${approval.approval_request_id}:${stage}`;
      const fingerprint = canonicalJSONStringify({ project_id: projectID, approval_request_id: approval.approval_request_id, run_id: approval.run_id, scope: approval.scope, version: approval.version, subject_ref_id: approval.subject_ref_id, subject_snapshot_hash: approval.subject_snapshot_hash, action, instruction, resolutionPayload, payload });
      const previous = approvalRequestKeys.current.get(slot);
      if (previous?.fingerprint === fingerprint) return previous.key;
      const key = createUUID();
      approvalRequestKeys.current.set(slot, { fingerprint, key });
      return key;
    };
    if (action === "edit_artifact") {
      const target = artifacts.find((artifact) =>
        artifact.current_version_id === approval.subject_ref_id ||
        artifact.step_run_id === approval.subject_ref_id,
      );
      if (!target) throw new ApiError("ARTIFACT_NOT_FOUND", "没有找到该确认对应的可编辑产物。", 404);
      setSelectedArtifact(target.artifact_id);
      setEditRequestedArtifactID(target.artifact_id);
      return;
    }
    if (approval.scope === "quality_review" && action === "manual_edit") {
      const review = await api.getQualityReview(approval.subject_ref_id);
      if (!approval.run_id || review.run_id !== approval.run_id || review.project_id !== projectID ||
          review.quality_review_id !== approval.subject_ref_id || review.review_version !== approval.subject_version ||
          review.input_snapshot_hash !== approval.subject_snapshot_hash || !Array.isArray(review.affected_episode_nos)) {
        throw new ApiError("APPROVAL_SUBJECT_CHANGED", "审核内容与当前确认版本不符。", 409);
      }
      const affected = new Set(review.affected_episode_nos);
      const target = artifacts.find((artifact) => {
        const episodeNo = Number(artifact.scope_key?.match(/\d+/)?.[0] ?? 0);
        return artifact.run_id === approval.run_id && artifact.artifact_type === "script_unit" && (!affected.size || affected.has(episodeNo));
      });
      if (!target) throw new ApiError("ARTIFACT_NOT_FOUND", "没有找到质量审核对应的可编辑单集剧本。", 404);
      setSelectedArtifact(target.artifact_id);
      setEditRequestedArtifactID(target.artifact_id);
      return;
    }
    try {
    if (approval.scope === "final_selection" && action === "select_final") {
      const previousID = finalSelection?.final_selection_id ?? null;
      const result = await api.confirmFinalSelection(project.project_id, approval, previousID, requestKey("final", previousID));
      const selected = result?.final_selection, candidate = result?.candidate, resolved = result?.approval;
      if (!selected?.final_selection_id || selected.project_id !== projectID || selected.candidate_id !== approval.subject_ref_id || selected.approval_request_id !== approval.approval_request_id || selected.replaced_selection_id !== previousID || selected.status !== "active" || !Number.isSafeInteger(selected.selection_no) || selected.selection_no < 1 || candidate?.candidate_id !== approval.subject_ref_id || candidate.project_id !== projectID || candidate.source_run_id !== approval.run_id || candidate.status !== "final" || resolved?.approval_request_id !== approval.approval_request_id || resolved.project_id !== projectID || resolved.subject_snapshot_hash !== approval.subject_snapshot_hash || resolved.subject_ref_id !== approval.subject_ref_id || resolved.scope !== "final_selection" || resolved.status !== "resolved") throw new ApiError("CANDIDATE_RECEIPT_INVALID", "最终稿确认回执不一致，请刷新确认。", 502);
    }
    else if (approval.scope === "quality_review" && action === "accept_with_risk") await api.acceptQualityReviewRisk(approval, requestKey("risk"));
    else if (approval.scope === "quality_review" && (action === "ai_revise" || action === "confirm_change")) await api.resolveQualityReviewAction(approval, action, instruction, requestKey("quality"));
    else if (approval.scope === "quality_review") throw new ApiError("ACTION_UNSUPPORTED", "当前质量审核操作不受支持。", 400);
    else if (action === "request_ai_revision") {
      const target = resolutionPayload && typeof resolutionPayload === "object" && !Array.isArray(resolutionPayload)
        ? (resolutionPayload as { target_artifact_version_id?: unknown }).target_artifact_version_id : undefined;
      if ((target !== undefined && (typeof target !== "string" || !target.trim())) ||
          (approval.subject_kind === "artifact_version_set" && typeof target !== "string")) {
        throw new ApiError("TARGET_CANDIDATE_INVALID", "请选择要修改的确切产物版本。", 400);
      }
      const versionID = typeof target === "string" ? target : undefined;
      const result = await api.requestApprovalRegeneration<RevisionRequest>(approval, action, instruction, requestKey("regenerate"), versionID);
      const expectedBase = versionID ?? (approval.subject_kind === "artifact_version" ? approval.subject_ref_id : undefined);
      if (typeof result?.revision_request_id !== "string" || !result.revision_request_id || result.project_id !== projectID ||
          result.conversation_id !== project.primary_conversation_id || typeof result.artifact_id !== "string" || !result.artifact_id ||
          !Number.isSafeInteger(result.version) || result.version < 1 ||
          !["queued", "running", "waiting_safe_checkpoint", "proposed", "no_change", "failed", "accepted", "rejected", "cancelled"].includes(result.status) ||
          result.base_artifact_version_id !== expectedBase || result.instruction !== instruction.trim() ||
          (result.source_approval_request_id && result.source_approval_request_id !== approval.approval_request_id)) {
        throw new ApiError("REVISION_RECEIPT_INVALID", "返修回执与所选产物版本不一致，请核对后重试。", 502);
      }
      return;
    }
    else if (action === "regenerate_artifact") await api.requestApprovalRegeneration(approval, action, instruction, requestKey("regenerate"));
    else await api.resolveApproval(approval, action, resolutionPayload, requestKey("resolve"));
    ++refreshSequence.current;
    if (approval.run_id && approval.scope !== "final_selection") {
      const resolvedRun = await api.getRunSnapshot(approval.run_id);
      if (resolvedRun.run.run_id !== approval.run_id) throw new ApiError("RUN_STATE_CONFLICT", "确认已提交，但读取的运行状态不匹配，请刷新后继续。", 409);
      const resumeAction = resolvedRun.available_actions.find((item) => item.enabled && item.action_id === "resume_run" && item.target_type === "run" && item.target_id === approval.run_id);
      if (resumeAction) await api.executeRunAction(approval.run_id, resumeAction, requestKey("resume", approval.run_id));
    }
    } finally {
      await load();
    }
  };
  const resolveAgentToolApproval = async (approval: AgentToolApproval, action: "approve" | "reject") => {
    const call = agentToolCalls.find((item) => item.agent_tool_call_id === approval.agent_tool_call_id);
    if (principal.role === "viewer" || approval.project_id !== projectID || approval.workspace_id !== principal.workspace_id || !call || call.project_id !== projectID || call.approval?.agent_tool_approval_id !== approval.agent_tool_approval_id || call.approval.version !== approval.version || call.approval.subject_snapshot_hash !== approval.subject_snapshot_hash || call.approval.status !== "pending" || !call.approval.options.includes(action)) throw new ApiError("AGENT_TOOL_APPROVAL_SUBJECT_CHANGED", "工具审批或权限已经变化，请刷新后重试。", 409);
    if (toolApprovalSubmitting.current.has(approval.agent_tool_approval_id)) throw new ApiError("COMMAND_IN_PROGRESS", "该工具审批正在提交。", 409);
    const subject = canonicalJSONStringify([projectID, approval.agent_tool_call_id, approval.version, approval.subject_snapshot_hash]);
    const pending = toolApprovalPendingActions.current.get(approval.agent_tool_approval_id);
    if (pending?.subject === subject && pending.action !== action) throw new ApiError("TOOL_APPROVAL_OUTCOME_UNKNOWN", "前一次审批结果尚未确认，请先重试原操作或刷新确认。", 409);
    const slot = `${approval.agent_tool_approval_id}:${action}`;
    const fingerprint = canonicalJSONStringify([projectID, approval.agent_tool_call_id, approval.version, approval.subject_snapshot_hash, action]);
    if (toolApprovalRequestKeys.current.get(slot)?.fingerprint !== fingerprint) toolApprovalRequestKeys.current.set(slot, { fingerprint, key: createUUID() });
    toolApprovalSubmitting.current.add(approval.agent_tool_approval_id);
    toolApprovalPendingActions.current.set(approval.agent_tool_approval_id, { subject, action });
    ++refreshSequence.current;
    try {
      const updated = await api.resolveAgentToolApproval(approval, action, toolApprovalRequestKeys.current.get(slot)!.key);
      if (updated.agent_tool_approval_id !== approval.agent_tool_approval_id || updated.agent_tool_call_id !== approval.agent_tool_call_id || updated.workspace_id !== approval.workspace_id || updated.project_id !== projectID || updated.conversation_id !== approval.conversation_id || updated.version !== approval.version + 1 || updated.subject_snapshot_hash !== approval.subject_snapshot_hash || updated.status !== (action === "approve" ? "approved" : "rejected")) throw new ApiError("TOOL_APPROVAL_RECEIPT_INVALID", "工具审批回执不一致，请刷新确认。", 502);
      ++refreshSequence.current;
      setAgentToolCalls((items) => items.map((item) => item.agent_tool_call_id === approval.agent_tool_call_id && item.approval?.agent_tool_approval_id === approval.agent_tool_approval_id && item.approval.version <= approval.version ? { ...item, approval: updated, approval_status: updated.status, status: item.status === "pending_approval" ? updated.status : item.status } : item));
      toolApprovalPendingActions.current.delete(approval.agent_tool_approval_id);
    } catch (error) {
      if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") toolApprovalPendingActions.current.delete(approval.agent_tool_approval_id);
      throw error;
    } finally {
      try { await load(); } finally { toolApprovalSubmitting.current.delete(approval.agent_tool_approval_id); }
    }
  };
  const executeRunAction = async (action: AvailableAction) => {
    const runID = runSnapshot?.run.run_id;
    const identity = runActionIdentity(action);
    if (principal.role === "viewer" || !runSnapshot || !runID || runID !== project.active_write_run_id || !runActionMatchesSnapshot(runSnapshot, action) || !runSnapshot.available_actions.some((current) => current.enabled && runActionIdentity(current) === identity)) {
      await load();
      throw new ApiError("RUN_STATE_CONFLICT", "运行或操作权限已经变化，请刷新后重试。", 409);
    }
    if (runActionSubmitting.current.has(runID)) throw new ApiError("RUN_CONTROL_BUSY", "该运行的操作正在提交。", 409);
    const keys = runActionRequestKeys.current.get(runID) ?? new Map<string, string>();
    if (!keys.has(identity)) keys.set(identity, createUUID());
    runActionRequestKeys.current.set(runID, keys);
    runActionSubmitting.current.add(runID);
    ++refreshSequence.current;
    try {
      const snapshot = await api.executeRunAction(runID, action, keys.get(identity)!);
      if (snapshot.run?.run_id !== runID) throw new ApiError("RUN_CONTROL_RECEIPT_INVALID", "运行操作回执不一致，请刷新确认。", 502);
      runActionRequestKeys.current.delete(runID);
    } catch (error) {
      if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408) keys.delete(identity);
      throw error;
    } finally {
      runActionSubmitting.current.delete(runID);
      await load();
    }
  };
  const completePendingScriptEdit = async (pending: PendingScriptEdit) => {
    const versions = pending.expected_script_version_ids;
    if (principal.role === "viewer" || !pending.can_complete || pending.project_id !== projectID || pending.run_id !== project.active_write_run_id || pending.run_id !== runSnapshot?.run.run_id || pending.step_run_id !== runSnapshot.run.current_step_run_id || !Array.isArray(versions) || !versions.length || versions.some((id) => typeof id !== "string" || !id) || new Set(versions).size !== versions.length) throw new ApiError("SCRIPT_EDIT_STATE_CONFLICT", "交接状态已经变化，请刷新后重试。", 409);
    const slot = `${pending.run_id}:${pending.step_run_id}`;
    const fingerprint = canonicalJSONStringify({ project_id: projectID, run_id: pending.run_id, step_run_id: pending.step_run_id, versions });
    if (scriptEditRequestKeys.current.get(slot)?.fingerprint !== fingerprint) scriptEditRequestKeys.current.set(slot, { fingerprint, key: createUUID() });
    if (scriptEditSubmitting.current.has(pending.run_id)) return;
    scriptEditSubmitting.current.add(pending.run_id);
    ++refreshSequence.current;
    try {
      const result = await api.completeScriptEdit(pending.run_id, [...versions], scriptEditRequestKeys.current.get(slot)!.key);
      if (result.run_snapshot?.run.run_id !== pending.run_id || !Array.isArray(result.refresh_task_ids) || result.refresh_task_ids.length !== versions.length || result.refresh_task_ids.some((id) => !id) || new Set(result.refresh_task_ids).size !== versions.length) throw new ApiError("SCRIPT_EDIT_RECEIPT_INVALID", "交接更新回执不一致，请刷新确认。", 502);
    } finally {
      scriptEditSubmitting.current.delete(pending.run_id);
      await load();
    }
  };
  const updateProposedAction = (action: ProposedAction) => {
    rememberProposedAction(action);
    void load();
  };
  const startProposedRun = async (action: ProposedAction) => {
    await startProposal(action, false);
  };
  const updateAgentTask = (task: AgentTask) => setAgentTasks((items) => [...items.filter((item) => item.agent_task_id !== task.agent_task_id), task]);
  const startProposedAgentTask = async (action: ProposedAction) => {
    await startProposal(action, true);
  };
  const startProposal = async (action: ProposedAction, background: boolean) => {
    const current = proposedActions.find((item) => item.proposed_action_id === action.proposed_action_id);
    if (principal.role === "viewer" || !current || current.status !== "pending" || current.version !== action.version || current.snapshot_hash !== action.snapshot_hash || action.status !== "pending" || action.action_type !== (background ? "start_background_task" : "start_run")) throw new ApiError("CONFIRMATION_ACTION_MISMATCH", "启动确认已变化，请刷新后重试。", 409);
    if (proposalSubmitting.current.has(action.proposed_action_id)) throw new ApiError("COMMAND_IN_PROGRESS", "该启动确认正在提交。", 409);
    proposalSubmitting.current.add(action.proposed_action_id);
    ++refreshSequence.current;
    try {
      let runID: string | undefined;
      let taskID: string | undefined;
      if (background) {
        const task = await api.startAgentTask(project, action, proposalRequestKey("start_task", action, project.primary_conversation_id));
        if (!task.agent_task_id || task.project_id !== projectID || task.conversation_id !== project.primary_conversation_id || task.proposed_action_id !== action.proposed_action_id || task.capability_id !== action.capability_ref.capability_id || task.capability_version !== action.capability_ref.version) throw new ApiError("START_RECEIPT_INVALID", "启动回执与当前确认不一致，请刷新确认。", 502);
        taskID = task.agent_task_id;
        updateAgentTask(task);
      } else {
        const snapshot = await api.startRun(project, action, proposalRequestKey("start_run", action, project.primary_conversation_id));
        if (!snapshot.run?.run_id || snapshot.run.project_id !== projectID || snapshot.run.conversation_id !== project.primary_conversation_id || snapshot.run.capability_id !== action.capability_ref.capability_id || snapshot.run.capability_version !== action.capability_ref.version) throw new ApiError("START_RECEIPT_INVALID", "启动回执与当前确认不一致，请刷新确认。", 502);
        runID = snapshot.run.run_id;
        setRunSnapshot(snapshot);
      }
      ++refreshSequence.current;
      setProposedActions((items) => items.map((item) => item.proposed_action_id === action.proposed_action_id && item.version === action.version && item.snapshot_hash === action.snapshot_hash ? { ...item, status: "consumed", consumed_run_id: runID, consumed_task_id: taskID } : item));
    } finally {
      proposalSubmitting.current.delete(action.proposed_action_id);
      await load();
    }
  };
  const controlAgentTask = async (task: AgentTask, operation: "cancel" | "pause" | "resume" | "retry") => {
    const current = agentTasks.find((item) => item.agent_task_id === task.agent_task_id);
    if (principal.role === "viewer" || task.project_id !== projectID || !current || current.status !== task.status || current.attempt_count !== task.attempt_count) throw new ApiError("AGENT_TASK_STATE_CONFLICT", "任务或操作权限已经变化，请刷新后重试。", 409);
    if (taskControlSubmitting.current.has(task.agent_task_id)) throw new ApiError("COMMAND_IN_PROGRESS", "该后台任务的操作正在提交。", 409);
    const keys = taskControlRequestKeys.current.get(task.agent_task_id) ?? new Map<string, string>();
    if (!keys.has(operation)) keys.set(operation, createUUID());
    taskControlRequestKeys.current.set(task.agent_task_id, keys);
    taskControlSubmitting.current.add(task.agent_task_id);
    ++refreshSequence.current;
    try {
      const methods = { cancel: api.cancelAgentTask, pause: api.pauseAgentTask, resume: api.resumeAgentTask, retry: api.retryAgentTask };
      const result = await methods[operation](task.agent_task_id, keys.get(operation)!);
      if (result.agent_task_id !== task.agent_task_id || result.project_id !== projectID || result.conversation_id !== task.conversation_id) throw new ApiError("TASK_CONTROL_RECEIPT_INVALID", "后台任务操作回执不一致，请刷新确认。", 502);
      taskControlRequestKeys.current.delete(task.agent_task_id);
    } catch (error) {
      if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") keys.delete(operation);
      throw error;
    } finally {
      taskControlSubmitting.current.delete(task.agent_task_id);
      await load();
    }
  };
  const cancelAgentTask = (task: AgentTask) => controlAgentTask(task, "cancel");
  const pauseAgentTask = (task: AgentTask) => controlAgentTask(task, "pause");
  const appendAgentTaskInput = async (task: AgentTask, content: string, idempotencyKey: string, refs?: AttachmentRef[]) => { if (refs?.length) await api.appendAgentTaskInput(task.agent_task_id, content, idempotencyKey, refs); else await api.appendAgentTaskInput(task.agent_task_id, content, idempotencyKey); await load(); };
  const resumeAgentTask = (task: AgentTask) => controlAgentTask(task, "resume");
  const retryAgentTask = (task: AgentTask) => controlAgentTask(task, "retry");

  const hasPendingAgentToolApproval = agentToolCalls.some((call) => call.approval?.status === "pending");
  return (
    <div className="workbench-page">
      <a className="skip-link" href="#workbench-main">跳到主要内容</a>
      <BrandHeader />
      <div className="workbench-topbar">
        <a className="back-button" href="/"><ArrowLeft size={16} />作品</a>
        <div className="workbench-title">
          <strong>{project.title}</strong>
          <span>{hasPendingAgentToolApproval ? "等待工具授权" : runSnapshot ? runStatusLabel(runSnapshot.run.status) : approvals.length ? "等待确认" : projectStatus(project)}</span>
        </div>
        <CandidateControls project={project} candidates={candidates} artifacts={artifacts} selection={finalSelection} capabilities={capabilities} readOnly={principal.role === "viewer"} onChanged={load} onPreview={(version) => {
          if (artifactDirty && !window.confirm("当前产物有未保存修改。放弃修改并切换吗？")) return false;
          setArtifactDirty(false); setPreviewRevision(null); setCandidatePreviewVersion(version); setSelectedArtifact(version.artifact_id); setArtifactVersion(version); return true;
        }} />
        <ProjectFiles projectID={projectID} principal={principal} onInstalled={() => load(false, true)} revisionKey={agentToolCalls.filter((call) => call.tool_name === "apply_workspace_patch" || call.tool_name === "publish_workspace_files").map((call) => `${call.agent_tool_call_id}:${call.status}`).join("|")} />
        <ProjectMemory projectID={projectID} principal={principal} />
        <a className="icon-button" href={`/instructions?project_id=${encodeURIComponent(projectID)}`} target="_blank" rel="noopener noreferrer" aria-label="作品规则" title="作品规则"><FileText size={17} /></a>
        <a className="icon-button workbench-skills-link" href="/skills" target="_blank" rel="noopener noreferrer" aria-label="Skill 管理" title="Skill 管理"><Settings2 size={17} /></a>
      </div>
      {failure && <ConnectionFailure message={failure} retry={load} />}
      <div className="mobile-pane-tabs" role="tablist" aria-label="工作台区域">
        <button type="button" role="tab" aria-selected={mobilePane === "artifacts"} onClick={() => setMobilePane("artifacts")}><FolderOpen size={15} />产物</button>
        <button type="button" role="tab" aria-selected={mobilePane === "workspace"} onClick={() => setMobilePane("workspace")}><FileText size={15} />工作区</button>
        <button type="button" role="tab" aria-selected={mobilePane === "agent"} onClick={() => setMobilePane("agent")}><MessageSquare size={15} />Agent</button>
      </div>
      <main className={`workbench-grid mobile-${mobilePane}`} id="workbench-main">
        <ArtifactRail project={project} artifacts={artifacts} presentations={artifactPresentations} activities={activities} capabilities={capabilities} selected={selectedArtifact} onSelect={(artifactID) => { selectArtifact(artifactID); setMobilePane("workspace"); }} />
        <ArtifactWorkspace key={artifactWorkspaceKey} artifact={activeArtifact} projectAssets={projectAssets} presentations={artifactPresentations} runArtifacts={artifacts.filter((item) => item.run_id === activeArtifact?.run_id)} version={currentArtifactVersion} readOnly={principal.role === "viewer"} proposal={previewRevision?.artifact_id === activeArtifact?.artifact_id ? previewRevision : null} onCloseProposal={() => setPreviewRevision(null)} failure={artifactFailure} hasArtifacts={artifacts.some((item) => artifactIsVisible(artifactPresentations, item))} editRequested={editRequestedArtifactID === activeArtifact?.artifact_id} onEditRequestHandled={() => setEditRequestedArtifactID(null)} onDirtyChange={setArtifactDirty} onSelectionChange={setTargetSelection} onDisplayedVersionChange={setDisplayedArtifactVersion} onChanged={load} />
        <AgentPanel
          project={project}
          goal={projectGoal}
          projectAssets={projectAssets}
          messages={messages}
          agentTurns={agentTurns}
          agentTasks={agentTasks}
          agentToolCalls={agentToolCalls}
          agentTurnLive={agentTurnLive}
          activities={activities}
          approvals={approvals}
          approvalRegistry={approvalRegistry}
          taskRegistry={taskRegistry}
          interactionRegistry={interactionRegistry}
          proposedActions={proposedActions}
          revisions={revisionRequests}
          resolutions={targetResolutions}
          capabilities={capabilities}
          runSnapshot={runSnapshot}
          viewedRunID={activeArtifact?.run_id ?? null}
          streamState={streamState}
          composerContext={composerContext}
          onClearSelection={() => setTargetSelection(null)}
          onSend={send}
          onCancelAgentTurn={cancelAgentTurn}
          onControlAgentTurnPause={controlAgentTurnPause}
          onAppendAgentTurnInput={appendAgentTurnInput}
          onEditQueuedAgentTurn={editQueuedAgentTurn}
          onResolveApproval={resolveApproval}
          onResolveAgentToolApproval={resolveAgentToolApproval}
          onConfigureAction={updateProposedAction}
          onStartRun={startProposedRun}
          onStartAgentTask={startProposedAgentTask}
          onCancelAgentTask={cancelAgentTask}
          onPauseAgentTask={pauseAgentTask}
          onAppendAgentTaskInput={appendAgentTaskInput}
          onResumeAgentTask={resumeAgentTask}
          onRetryAgentTask={retryAgentTask}
          onOpenTaskResult={selectArtifact}
          onRunAction={executeRunAction}
          onCompleteScriptEdit={completePendingScriptEdit}
          onResolveRevisionTarget={resolveRevisionTarget}
          onExecuteRevision={executeRevision}
          onPreviewRevision={previewRevisionDraft}
          onAcceptRevision={acceptRevision}
          onRejectRevision={rejectRevision}
          onCancelRevision={cancelRevision}
        />
      </main>
    </div>
  );
}

function runStatusLabel(status: string) { return ({ running: "生成中", pausing: "暂停中", paused: "已暂停", failed: "生成失败", waiting_approval: "等待确认", completed: "已完成", cancelled: "已结束" } as Record<string, string>)[status] ?? "处理中"; }

export function CandidateControls({ project, candidates, artifacts, selection, capabilities, readOnly = false, onChanged, onPreview }: { project: Project; candidates: ScriptCandidate[]; artifacts: Artifact[]; selection: FinalSelection | null; capabilities: ComposerRegistryEntry[]; readOnly?: boolean; onChanged: () => Promise<void>; onPreview: (version: ArtifactVersion) => boolean }) {
  const [open, setOpen] = useState(false);
  const [exportOpen, setExportOpen] = useState(false);
  const [selectedID, setSelectedID] = useState(selection?.candidate_id ?? candidates[0]?.candidate_id ?? "");
  const [previewedVersionID, setPreviewedVersionID] = useState<string | null>(null);
  const [busy, setBusy] = useState("");
  const [failure, setFailure] = useState("");
  const [pending, setPending] = useState("");
  const submitting = useRef(false);
  const mounted = useRef(true);
  const controller = useRef<AbortController | null>(null);
  const command = useRef<{ subject: string; action: string; key: string } | null>(null);
  const selected = candidates.find((item) => item.candidate_id === selectedID) ?? null;
  const subject = canonicalJSONStringify([project.project_id, readOnly, selected?.candidate_id, selected?.project_id, selected?.source_run_id, selected?.scripts_artifact_version_id, selected?.status, selection?.final_selection_id]);
  const current = useRef({ subject, candidates, artifacts });
  current.current = { subject, candidates, artifacts };
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; controller.current?.abort(); }; }, []);
  useEffect(() => { if (command.current?.subject !== subject) { command.current = null; setPending(""); setFailure(""); } }, [subject]);
  useEffect(() => {
    if (!candidates.some((item) => item.candidate_id === selectedID)) { setSelectedID(selection?.candidate_id ?? candidates[0]?.candidate_id ?? ""); setPreviewedVersionID(null); }
  }, [candidates, selectedID, selection?.candidate_id]);
  useEffect(() => {
    if (!open && !exportOpen) return;
    const close = (event: PointerEvent) => {
      if (!submitting.current && !(event.target as Element).closest(".topbar-tools")) { setOpen(false); setExportOpen(false); }
    };
    const escape = (event: KeyboardEvent) => {
      if (!submitting.current && event.key === "Escape") { setOpen(false); setExportOpen(false); }
    };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", escape);
    return () => { document.removeEventListener("pointerdown", close); document.removeEventListener("keydown", escape); };
  }, [open, exportOpen]);
  const previewChanged = previewedVersionID !== null && previewedVersionID !== selected?.scripts_artifact_version_id;
  const versionWarning = previewChanged ? "候选稿已有新版本，请重新选择后再导出或定稿。" : "";
  const available = !previewChanged && selected?.project_id === project.project_id && ["candidate", "final", "historical_final"].includes(selected.status);
  const blocked = (action: string) => readOnly || !available || Boolean(busy) || Boolean(pending && pending !== action);
  const choose = async (candidate: ScriptCandidate) => {
    if (submitting.current || pending) return;
    submitting.current = true; setBusy("preview"); setFailure("");
    try {
      if (candidate.project_id !== project.project_id) throw new ApiError("CANDIDATE_CONTEXT_INVALID", "候选稿不属于当前作品。", 409);
      const version = await api.getArtifactVersion(candidate.scripts_artifact_version_id);
      if (!mounted.current || current.current.subject !== subject) return;
      const known = current.current.candidates.find((item) => item.candidate_id === candidate.candidate_id);
      const artifact = current.current.artifacts.find((item) => item.artifact_id === version.artifact_id);
      if (known?.scripts_artifact_version_id !== candidate.scripts_artifact_version_id || version.artifact_version_id !== candidate.scripts_artifact_version_id || artifact?.project_id !== project.project_id || artifact.artifact_type !== "scripts" || artifact.run_id !== candidate.source_run_id || !["confirmed", "superseded"].includes(version.status)) throw new ApiError("CANDIDATE_CONTEXT_INVALID", "候选稿内容与所选版本不一致，请刷新。", 409);
      if (onPreview(version)) { setSelectedID(candidate.candidate_id); setPreviewedVersionID(version.artifact_version_id); }
    } catch (error) { if (mounted.current && current.current.subject === subject) setFailure(errorText(error)); }
    finally { submitting.current = false; if (mounted.current) setBusy(""); }
  };
  const perform = async (action: "final" | "txt" | "docx") => {
    if (!selected || blocked(action) || submitting.current) return;
    submitting.current = true; setBusy(action); setPending(action); setFailure("");
    if (command.current?.subject !== subject || command.current.action !== action) command.current = { subject, action, key: createUUID() };
    const key = command.current.key;
    const active = () => mounted.current && current.current.subject === subject;
    try {
      if (action === "final") {
        const expectedSelection = selection?.final_selection_id ?? null;
        const result = await api.previewFinalSelection(project.project_id, selected.candidate_id, expectedSelection, key, selected.scripts_artifact_version_id);
        if (!active()) return;
        const preview = result?.preview, approval = result?.approval;
        if (!preview?.final_selection_preview_id || preview.project_id !== project.project_id || preview.candidate_id !== selected.candidate_id || preview.current_selection_id !== expectedSelection || preview.expected_current_selection_id !== expectedSelection || !["pending", "consumed"].includes(preview.status) || !preview.preview_hash || preview.approval_request_id !== approval?.approval_request_id || approval.project_id !== project.project_id || approval.run_id !== selected.source_run_id || approval.scope !== "final_selection" || approval.subject_ref_id !== selected.candidate_id || approval.subject_snapshot_hash !== preview.preview_hash || approval.status !== (preview.status === "pending" ? "pending" : "resolved")) throw new ApiError("CANDIDATE_RECEIPT_INVALID", "最终稿预览回执不一致，请刷新确认。", 502);
        setOpen(false);
      } else {
        const result = await api.createScriptExport(selected, action, key);
        if (!active()) return;
        if (!result?.export_id || result.project_id !== project.project_id || result.candidate_id !== selected.candidate_id || result.artifact_version_id !== selected.scripts_artifact_version_id || result.format !== action || result.status !== "ready" || !result.filename || !Number.isSafeInteger(result.size_bytes) || result.size_bytes < 1 || !/^[a-f0-9]{64}$/i.test(result.checksum)) throw new ApiError("CANDIDATE_RECEIPT_INVALID", "导出回执与候选稿版本不一致，请刷新确认。", 502);
        const abort = new AbortController(); controller.current = abort;
        const blob = await api.downloadScriptExport(result.export_id, abort.signal);
        if (!active()) return;
        if (blob.size !== result.size_bytes) throw new ApiError("EXPORT_CONTENT_INVALID", "下载内容与导出回执不一致，请重试。", 502);
        const bytes = await blob.arrayBuffer();
        const digest = await crypto.subtle.digest("SHA-256", bytes);
        if (!active()) return;
        if ([...new Uint8Array(digest)].map((value) => value.toString(16).padStart(2, "0")).join("") !== result.checksum.toLowerCase()) throw new ApiError("EXPORT_CONTENT_INVALID", "下载内容校验失败，请重试。", 502);
        const url = URL.createObjectURL(blob), link = document.createElement("a");
        link.href = url; link.download = result.filename; document.body.append(link); link.click(); link.remove();
        window.setTimeout(() => URL.revokeObjectURL(url), 1000);
        setExportOpen(false);
      }
      setPending("");
    } catch (error) {
      if (active()) {
        setFailure(errorText(error));
        if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") { command.current = null; setPending(""); }
      }
    } finally {
      try { await onChanged(); }
      catch (error) { if (active()) { setFailure(errorText(error)); if (action === "final") setOpen(true); else setExportOpen(true); } }
      finally { submitting.current = false; controller.current = null; if (mounted.current) setBusy(""); }
    }
  };
  return <div className="topbar-tools">
    <div className="candidate-control">
      <button className="secondary-button menu-trigger candidate-trigger" disabled={!candidates.length || Boolean(busy)} aria-haspopup="menu" aria-expanded={open} onClick={() => { setExportOpen(false); setOpen(!open); }}><span>{selection ? "最终稿" : "候选稿"}</span><ChevronDown className="menu-chevron" size={15} /></button>
      {open && <div className="candidate-menu" role="menu"><header><strong>剧本候选</strong><span>{candidates.length} 份</span></header>
        {candidates.map((candidate) => <button role="menuitem" key={candidate.candidate_id} disabled={Boolean(busy || pending)} className={candidate.candidate_id === selectedID ? "selected" : ""} onClick={() => void choose(candidate)}><span><strong>{candidate.label}</strong><small>{capabilityLabel(capabilities, candidate.source_capability_id, "其他生成链路")} · {formatTime(candidate.updated_at)}</small></span>{selection?.candidate_id === candidate.candidate_id ? <span className="final-mark">最终稿</span> : candidate.candidate_id === selectedID ? <Check size={15} /> : null}</button>)}
        {!readOnly && selected && selection?.candidate_id !== selected.candidate_id && <footer><button className="primary-button" disabled={blocked("final")} onClick={() => void perform("final")}>{busy === "final" ? "正在提交…" : "设为最终稿"}</button></footer>}
        {(failure || versionWarning) && <p className="form-error" role="alert">{failure || versionWarning}</p>}
      </div>}
    </div>
    {!readOnly && <div className="export-control"><button className="primary-button menu-trigger export-trigger" disabled={!available || Boolean(busy)} aria-haspopup="menu" aria-expanded={exportOpen} onClick={() => { setOpen(false); setExportOpen(!exportOpen); }}><Download className="menu-leading-icon" size={16} /><span>{busy === "txt" || busy === "docx" ? "正在导出…" : "导出剧本"}</span><span className="menu-chevron-box"><ChevronDown className="menu-chevron" size={15} /></span></button>
      {exportOpen && <div className="export-menu" role="menu"><button role="menuitem" disabled={blocked("txt")} onClick={() => void perform("txt")}><strong>TXT 文本</strong></button><button role="menuitem" disabled={blocked("docx")} onClick={() => void perform("docx")}><strong>DOCX 文档</strong></button>{(failure || versionWarning) && <p className="form-error" role="alert">{failure || versionWarning}</p>}</div>}
    </div>}
  </div>;
}

function ArtifactRail({ project, artifacts, presentations, activities, capabilities, selected, onSelect }: { project: Project; artifacts: Artifact[]; presentations: ArtifactPresentation[]; activities: ProjectActivity[]; capabilities: ComposerRegistryEntry[]; selected: string | null; onSelect: (id: string) => void }) {
  const [runMenuOpen, setRunMenuOpen] = useState(false);
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(() => new Set());
  useEffect(() => {
    if (!runMenuOpen) return;
    const close = (event: PointerEvent) => { if (!(event.target as Element).closest(".rail-source")) setRunMenuOpen(false); };
    const escape = (event: KeyboardEvent) => event.key === "Escape" && setRunMenuOpen(false);
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", escape);
    return () => { document.removeEventListener("pointerdown", close); document.removeEventListener("keydown", escape); };
  }, [runMenuOpen]);
  const visibleArtifacts = artifacts.filter((item) => artifactIsVisible(presentations, item));
  const selectedArtifact = visibleArtifacts.find((item) => item.artifact_id === selected);
  const runGroups = useMemo(() => {
    const grouped = new Map<string, Artifact[]>();
    for (const artifact of visibleArtifacts) {
      const key = artifact.run_id ?? "project";
      grouped.set(key, [...(grouped.get(key) ?? []), artifact]);
    }
    return [...grouped.entries()].map(([runID, items]) => ({ runID, items: items.sort((left, right) => compareArtifacts(presentations, left, right)), updatedAt: items.reduce((latest, item) => item.updated_at > latest ? item.updated_at : latest, "") })).sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
  }, [artifacts, presentations]);
  const currentRunID = selectedArtifact?.run_id ?? runGroups[0]?.runID ?? "project";
  const currentRun = runGroups.find((group) => group.runID === currentRunID) ?? runGroups[0];
  const groups = groupArtifactsForRail(presentations, currentRun?.items ?? []);
  const capabilityForRun = (runID: string) => activities.find((item) => item.run_id === runID && item.capability_id)?.capability_id ?? (runID === project.active_write_run_id ? project.current_capability_id : null);
  const capabilityID = capabilityForRun(currentRunID) ?? project.latest_capability_id;
  const selectRun = (runID: string) => {
    const targetRun = runGroups.find((group) => group.runID === runID);
    const target = targetRun?.items.at(-1);
    if (target) onSelect(target.artifact_id);
    setRunMenuOpen(false);
  };
  return <aside className="artifact-rail"><div className="rail-heading"><span>流程与产物</span><button className="icon-button ghost" aria-label="更多流程操作"><MoreHorizontal size={17} /></button></div><div className="rail-source"><FileText size={16} /><button className="rail-run-selector" onClick={() => runGroups.length > 1 && setRunMenuOpen(!runMenuOpen)} aria-expanded={runMenuOpen}><span><strong>{capabilityID ? capabilityLabel(capabilities, capabilityID, "当前生成") : "通用 Agent"}</strong><small>{currentRun ? `${groups.length} 个步骤 · ${currentRun.items.length} 个产物` : "尚未创建产物"}</small></span>{runGroups.length > 1 && <ChevronDown size={14} />}</button>{runMenuOpen && <div className="rail-run-menu"><header>生成记录</header>{runGroups.map((run) => { const runCapability = capabilityForRun(run.runID); return <button key={run.runID} className={run.runID === currentRunID ? "selected" : ""} onClick={() => selectRun(run.runID)}><span><strong>{capabilityLabel(capabilities, runCapability, "通用 Agent")}</strong><small>{formatTime(run.updatedAt)} · {run.items.length} 个产物</small></span>{run.runID === currentRunID ? <Check size={14} /> : null}</button>; })}</div>}</div><nav className="artifact-list">{groups.length ? groups.map((group, index) => { const active = group.artifacts.some((item) => item.artifact_id === selected); const target = active ? group.artifacts.find((item) => item.artifact_id === selected)! : group.artifacts[0]; const stale = group.artifacts.filter((item) => item.status === "stale").length; const generated = group.artifacts.filter((item) => Boolean(item.current_version_id) && item.status !== "stale").length; const hasEpisodeDirectory = group.presentation.navigation.group_mode === "episode_directory" && group.artifacts.some((item) => scopeNumber(item.scope_key) > 0); const expanded = active && hasEpisodeDirectory && !collapsedGroups.has(group.key); const activateGroup = () => { if (active && hasEpisodeDirectory) { setCollapsedGroups((current) => { const next = new Set(current); if (next.has(group.key)) next.delete(group.key); else next.add(group.key); return next; }); return; } setCollapsedGroups((current) => { const next = new Set(current); next.delete(group.key); return next; }); onSelect(target.artifact_id); }; return <div className={`artifact-rail-group ${active ? "selected" : ""}`} key={group.key}><button className="artifact-group-button" aria-expanded={hasEpisodeDirectory ? expanded : undefined} onClick={activateGroup}><span className="step-index">{String(index + 1).padStart(2, "0")}</span><span><strong title={group.label}>{group.label}</strong><small>{hasEpisodeDirectory ? stale ? `${stale} 个待重新生成` : `${generated}/${group.artifacts.length} 已生成` : artifactStatusLabel(target)}</small></span>{hasEpisodeDirectory ? <span className="rail-group-end"><span className="rail-count">{group.artifacts.length}</span><ChevronDown className="rail-expand-icon" size={14} /></span> : target.status === "confirmed" ? <Check size={14} /> : null}</button>{expanded && <div className="artifact-episode-list" aria-label={`${group.label}分集目录`}>{[...group.artifacts].sort((left, right) => compareArtifacts(presentations, left, right)).map((item) => <button type="button" key={item.artifact_id} className={item.artifact_id === selected ? "selected" : ""} aria-current={item.artifact_id === selected ? "page" : undefined} onClick={() => onSelect(item.artifact_id)}><span>第 {scopeNumber(item.scope_key)} 集</span><small>{artifactStatusLabel(item)}</small></button>)}</div>}</div>; }) : <div className="rail-empty">在右侧描述目标，或选择一个 Skill 开始。</div>}</nav></aside>;
}

function scopeNumber(scope?: string | null) { return episodeNumberFromScope(scope) ?? 0; }
function artifactStatusLabel(artifact: Artifact) { if (artifact.status === "confirmed") return "已确认"; if (artifact.status === "pending_approval" || artifact.status === "draft") return "待确认"; if (artifact.status === "stale") return "待重新生成"; return artifact.current_version_id ? "已生成" : "生成中"; }

function ArtifactWorkspace({ artifact, projectAssets, presentations, runArtifacts, version: incomingVersion, readOnly, proposal, onCloseProposal, failure, hasArtifacts, editRequested, onEditRequestHandled, onDirtyChange, onSelectionChange, onDisplayedVersionChange, onChanged }: { artifact: Artifact | null; projectAssets: Asset[]; presentations: ArtifactPresentation[]; runArtifacts: Artifact[]; version: ArtifactVersion | null; readOnly: boolean; proposal: RevisionRequest | null; onCloseProposal: () => void; failure: string; hasArtifacts: boolean; editRequested: boolean; onEditRequestHandled: () => void; onDirtyChange: (dirty: boolean) => void; onSelectionChange: (selection: AgentTargetSelection | null) => void; onDisplayedVersionChange: (version: ArtifactVersion | null) => void; onChanged: () => Promise<void> }) {
  const [version, setVersion] = useState(incomingVersion);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<unknown>(() => clonePayload(incomingVersion?.payload ?? null));
  const [saving, setSaving] = useState(false);
  const submitting = useRef(false);
  const pendingSave = useRef<{ artifact: Artifact; base: ArtifactVersion; payload: unknown; key: string; completionKey: string; result?: VersionResult; completionDone?: boolean } | null>(null);
  const [savePending, setSavePending] = useState(false);
  const [saveFailure, setSaveFailure] = useState("");
  const [historyOpen, setHistoryOpen] = useState(false);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [historyFailure, setHistoryFailure] = useState("");
  const [versions, setVersions] = useState<ArtifactVersion[]>([]);
  const [previewVersion, setPreviewVersion] = useState<ArtifactVersion | null>(null);
  const [editorResetKey, setEditorResetKey] = useState(0);
  const presentation = presentationFor(presentations, artifact?.artifact_type ?? "");
  const editable = Boolean(version && presentation.editable && !readOnly && !failure);
  const renderer = rendererForArtifact(presentation);
  const isDirectScript = Boolean(editable && artifact && artifactUsesDirectEditor(presentation));
  const isDocumentEditor = Boolean(editable && artifact?.artifact_type === "generic_document" && !previewVersion && !proposal);
  const dirty = savePending || Boolean(version && (editing || isDirectScript || isDocumentEditor) && JSON.stringify(draft) !== JSON.stringify(version.payload));
  useEffect(() => { onDirtyChange(dirty); return () => onDirtyChange(false); }, [dirty, onDirtyChange]);
  useEffect(() => {
    if (!incomingVersion || dirty || submitting.current || incomingVersion.artifact_version_id === version?.artifact_version_id ||
        version && incomingVersion.version <= version.version) return;
    setVersion(incomingVersion); setEditing(false); setPreviewVersion(null); setDraft(clonePayload(incomingVersion.payload)); setSaveFailure("");
  }, [incomingVersion?.artifact_version_id, version?.artifact_version_id, dirty]);
  useEffect(() => {
    if (!editRequested || !editable) return;
    if (isDirectScript || isDocumentEditor) window.requestAnimationFrame(() => document.getElementById(isDocumentEditor ? "document-body" : "script-body")?.focus());
    else setEditing(true);
    onEditRequestHandled();
  }, [editRequested, editable, isDirectScript, isDocumentEditor, version?.artifact_version_id]);
  useEffect(() => { const warn = (event: BeforeUnloadEvent) => { if (dirty) event.preventDefault(); }; window.addEventListener("beforeunload", warn); return () => window.removeEventListener("beforeunload", warn); }, [dirty]);
  const openHistory = async () => {
    if (!artifact || historyLoading) return;
    setHistoryLoading(true); setHistoryFailure("");
    try { setVersions(await api.listArtifactVersions(artifact.artifact_id)); setHistoryOpen(true); }
    catch (error) { setHistoryFailure(errorText(error)); }
    finally { setHistoryLoading(false); }
  };
  const save = async () => {
    if (!artifact || !version || !editable || !dirty || submitting.current || previewVersion || proposal) return;
    submitting.current = true;
    const operation = pendingSave.current ?? { artifact: { ...artifact }, base: version, payload: clonePayload(draft), key: createUUID(), completionKey: createUUID() };
    pendingSave.current = operation; setSavePending(true);
    setSaving(true); setSaveFailure("");
    try {
      if (!operation.result) {
        const result = await api.createArtifactVersion(operation.artifact, operation.base, operation.payload, operation.key);
        if (result.artifact_version.artifact_id !== operation.artifact.artifact_id || !result.artifact_version.artifact_version_id ||
            !Number.isSafeInteger(result.artifact_version.version) || result.artifact_version.version <= operation.base.version) {
          throw new ApiError("ARTIFACT_VERSION_CONFLICT", "保存回执与当前产物不符。", 409);
        }
        operation.result = result;
      }
      const result = operation.result;
      if (result.handoff_refresh_required && !operation.completionDone) {
        if (!operation.artifact.run_id) throw new ApiError("RUN_NOT_FOUND", "剧本编辑缺少所属生成任务。", 409);
        const versions = result.pending_refresh_version_ids ?? ((result.pending_refresh_scopes?.length ?? 0) <= 1 ? [result.artifact_version.artifact_version_id] : []);
        if (!Array.isArray(versions) || !versions.includes(result.artifact_version.artifact_version_id) ||
            versions.some((id) => typeof id !== "string" || !id) || new Set(versions).size !== versions.length) {
          throw new ApiError("ARTIFACT_VERSION_CONFLICT", "保存回执缺少完整的待刷新版本集合。", 409);
        }
        const completion = await api.completeScriptEdit(operation.artifact.run_id, versions, operation.completionKey);
        if (completion.run_snapshot?.run?.run_id !== operation.artifact.run_id) {
          throw new ApiError("RUN_STATE_CONFLICT", "剧本刷新回执与当前生成任务不符。", 409);
        }
        operation.completionDone = true;
      }
      setVersion(result.artifact_version); setDraft(clonePayload(result.artifact_version.payload));
      await onChanged();
      pendingSave.current = null; setSavePending(false);
      setEditing(false);
    }
    catch (error) { setSaveFailure(errorText(error)); }
    finally { submitting.current = false; setSaving(false); }
  };
  const label = artifact ? presentation.label : "剧本工作区";
  const displayedVersion = proposal?.proposal_payload && version ? { ...version, payload: proposal.proposal_payload } : previewVersion ?? version;
  useEffect(() => { onDisplayedVersionChange(displayedVersion); }, [displayedVersion?.artifact_version_id, onDisplayedVersionChange]);
  const isScriptEditor = Boolean(isDirectScript && !previewVersion && !proposal);
  const cancelEditing = async () => {
    if (!version || submitting.current) return;
    const pending = pendingSave.current;
    if (pending && !window.confirm("本次保存可能已提交，刷新不会回滚已保存版本。确定放弃本地修改并刷新？")) return;
    const current = pending?.result?.artifact_version ?? incomingVersion ?? version;
    pendingSave.current = null; setSavePending(false); setVersion(current);
    setEditing(false);
    setDraft(clonePayload(current.payload));
    setEditorResetKey((current) => current + 1);
    setSaveFailure("");
    if (pending) {
      try { await onChanged(); } catch (error) { setSaveFailure(errorText(error)); }
    }
  };
  const changeDraft = (value: unknown) => { if (!submitting.current && !pendingSave.current && !readOnly) setDraft(value); };
  const pendingSaveStatus = savePending ? pendingSave.current?.result ? "正文已保存，后续处理待完成" : "保存结果待确认" : undefined;
  const isScriptCollection = renderer.kind === "script_collection";
  const selectionBase = artifact ? { artifactID: artifact.artifact_id, artifactType: artifact.artifact_type, artifactLabel: label, scopeKey: artifact.scope_key, entity: scopeNumber(artifact.scope_key) ? { episode_id: String(scopeNumber(artifact.scope_key)) } : undefined } : null;
  return (
    <section className={`artifact-workspace ${renderer.kind === "video_script" && isScriptEditor ? "video-script-artifact-workspace" : ""}`}>
      <div className="artifact-toolbar">
        <div><span className="eyebrow">当前工作区</span><h1>{label}</h1></div>
        {artifact && <div className="artifact-meta">
          <span>{proposal ? "Agent 修改稿" : displayedVersion ? `${previewVersion ? "历史" : "版本"} ${displayedVersion.version}` : "最新版本"}</span>
          {proposal ? <button className="secondary-button" onClick={onCloseProposal}>返回当前版本</button> : previewVersion ? <button className="secondary-button" onClick={() => setPreviewVersion(null)}>返回最新版</button> : editable && !editing && !isDirectScript && !isDocumentEditor ? <button className="secondary-button" onClick={() => setEditing(true)}>编辑内容</button> : null}
          {editing && !isScriptEditor && !isDocumentEditor && <>
            {saveFailure && <span className="toolbar-save-error" aria-live="polite">{saveFailure}</span>}
            <button className="secondary-button" onClick={cancelEditing}>取消</button>
            <button className="primary-button" disabled={saving || !dirty} onClick={() => void save()}>{saving ? "正在保存…" : "保存新版本"}</button>
          </>}
          {displayedVersion && <ArtifactDownloads artifactID={artifact.artifact_id} versionID={displayedVersion.artifact_version_id} disabledReason={proposal ? "修改稿尚未保存" : dirty ? "请先保存或取消修改" : saving ? "正在保存" : failure || displayedVersion.artifact_id !== artifact.artifact_id ? "产物版本尚未读取完成" : ""} />}
          <button className="secondary-button" disabled={historyLoading || savePending} aria-busy={historyLoading} onClick={() => void openHistory()}>{historyLoading ? <LoaderCircle size={15} className="spin" /> : <History size={15} />}版本历史</button>
        </div>}
      </div>
      {historyFailure && <p className="form-error" role="alert">{historyFailure}</p>}
      {artifact ? (
        <article className={`script-paper ${isScriptEditor || isDocumentEditor ? "script-editor-paper" : ""} ${isDocumentEditor ? "document-editor-paper" : ""} ${renderer.kind === "video_script" && isScriptEditor ? "video-script-paper" : ""} ${isScriptCollection ? "script-collection-paper" : ""}`}>
          {!isScriptEditor && !isDocumentEditor && <header><span>{proposal ? "修改预览，不会自动覆盖当前版本" : previewVersion ? "历史版本" : editing ? "正在编辑" : displayedVersion?.status === "confirmed" ? "已确认" : "待确认"}</span><strong>{scopeNumber(artifact.scope_key) ? `第 ${scopeNumber(artifact.scope_key)} 集 · ${label}` : label}</strong></header>}
          {failure ? <div className="paper-placeholder"><CircleAlert size={24} /><h2>正文读取失败</h2><p>{failure}</p></div> : displayedVersion ? (
            isDocumentEditor && version && selectionBase ? <DocumentArtifactEditor key={`${version.artifact_version_id}:${editorResetKey}`} payload={draft} dirty={dirty} saving={saving} locked={savePending} saveStatus={pendingSaveStatus} failure={saveFailure} onChange={changeDraft} onSave={() => void save()} onCancel={() => void cancelEditing()} selectionBase={selectionBase} onSelectionChange={onSelectionChange} />
              : isScriptEditor && version && selectionBase && renderer.kind === "video_script" ? <VideoScriptArtifactEditor key={`${version.artifact_version_id}:${editorResetKey}`} payload={draft} projectAssets={projectAssets} episodeNo={scopeNumber(artifact.scope_key)} dirty={dirty} saving={saving} locked={savePending} saveStatus={pendingSaveStatus} failure={saveFailure} onChange={changeDraft} onSave={() => void save()} onCancel={() => void cancelEditing()} selectionBase={selectionBase} onSelectionChange={onSelectionChange} />
              : isScriptEditor && version && selectionBase ? <ScriptArtifactEditor key={`${version.artifact_version_id}:${editorResetKey}`} payload={draft} episodeNo={scopeNumber(artifact.scope_key)} dirty={dirty} saving={saving} locked={savePending} saveStatus={pendingSaveStatus} failure={saveFailure} onChange={changeDraft} onSave={() => void save()} onCancel={() => void cancelEditing()} selectionBase={selectionBase} onSelectionChange={onSelectionChange} />
              : isScriptCollection ? <ScriptCollectionView key={displayedVersion.artifact_version_id} label={presentation.label} memberArtifactType={presentation.collection_member_type ?? "script_unit"} artifacts={runArtifacts} version={displayedVersion} />
                : <div className={editing ? "artifact-editor" : ""}><fieldset disabled={savePending || readOnly} style={{ border: 0, padding: 0, margin: 0, minWidth: 0 }}><ArtifactDocument artifactType={artifact.artifact_type} presentation={presentation} payload={editing ? draft : displayedVersion.payload} editing={editing && !readOnly} onChange={changeDraft} selectionBase={selectionBase ?? undefined} onSelectionChange={onSelectionChange} /></fieldset></div>
          ) : <div className="paper-placeholder"><span className="loading-mark" /><h2>正在读取最新版本…</h2></div>}
        </article>
      ) : <div className="workspace-empty"><span className="empty-symbol"><Sparkles size={22} /></span><h2>{hasArtifacts ? "选择左侧产物" : "从一个明确目标开始"}</h2><p>上传材料、引用能力，或直接告诉 Agent 你想完成什么剧本。</p></div>}
      {historyOpen && <VersionPanel versions={versions} currentID={displayedVersion?.artifact_version_id ?? ""} onSelect={(item) => { setPreviewVersion(item.artifact_version_id === version?.artifact_version_id ? null : item); setHistoryOpen(false); }} onClose={() => setHistoryOpen(false)} />}
    </section>
  );
}

function clonePayload<T>(payload: T): T { return payload == null ? payload : JSON.parse(JSON.stringify(payload)) as T; }

function VersionPanel({ versions, currentID, onSelect, onClose }: { versions: ArtifactVersion[]; currentID: string; onSelect: (version: ArtifactVersion) => void; onClose: () => void }) {
  useModalKeyboard();
  return <div className="modal-layer version-modal-layer" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
    <section className="dialog version-dialog" role="dialog" aria-modal="true" aria-labelledby="version-dialog-title">
      <div className="version-head"><div><span id="version-dialog-title">版本历史</span><small>{versions.length} 个版本</small></div><button className="icon-button ghost" aria-label="关闭" onClick={onClose}><X size={17} /></button></div>
      <div className="version-list">{[...versions].reverse().map((item) => <button type="button" key={item.artifact_version_id} className={item.artifact_version_id === currentID ? "current" : ""} onClick={() => onSelect(item)}><span>版本 {item.version}</span><strong>{item.status === "confirmed" ? "已确认" : item.artifact_version_id === currentID ? "正在查看" : "查看版本"}</strong><small>{formatTime(item.created_at)} · {item.creation_reason === "manual_edit" ? "手动编辑" : item.creation_reason === "generated" ? "Agent 生成" : "生成更新"}</small></button>)}</div>
    </section>
  </div>;
}

type AgentPanelProps = {
  project: Project;
  goal: unknown;
  projectAssets: Asset[];
  messages: Message[];
  agentTurns: AgentTurn[];
  agentTasks: AgentTask[];
  agentToolCalls: AgentToolCall[];
  agentTurnLive: Record<string, AgentTurnLiveState>;
  activities: ProjectActivity[];
  approvals: Approval[];
  approvalRegistry: WorkspaceRegistries["approvals"];
  taskRegistry: WorkspaceRegistries["tasks"];
  interactionRegistry: WorkspaceRegistries["interactions"];
  proposedActions: ProposedAction[];
  revisions: RevisionRequest[];
  resolutions: TargetResolution[];
  capabilities: ComposerRegistryEntry[];
  runSnapshot: RunSnapshot | null;
  viewedRunID: string | null;
  streamState: "connecting" | "connected" | "reconnecting";
  composerContext: AgentComposerContext;
  onClearSelection: () => void;
  onSend: SendMessage;
  onCancelAgentTurn: (turn: AgentTurn) => Promise<void>;
  onControlAgentTurnPause: (turn: AgentTurn, action: "pause" | "resume", idempotencyKey: string) => Promise<void>;
  onAppendAgentTurnInput: (turn: AgentTurn, content: string, idempotencyKey: string, attachments?: AttachmentRef[]) => Promise<void>;
  onEditQueuedAgentTurn: (turn: AgentTurn, content: string) => Promise<void>;
  onResolveApproval: (approval: Approval, action: string, instruction?: string, resolutionPayload?: unknown) => Promise<void>;
  onResolveAgentToolApproval: (approval: AgentToolApproval, action: "approve" | "reject") => Promise<void>;
  onConfigureAction: (action: ProposedAction) => void;
  onStartRun: (action: ProposedAction) => Promise<void>;
  onStartAgentTask: (action: ProposedAction) => Promise<void>;
  onCancelAgentTask: (task: AgentTask) => Promise<void>;
  onPauseAgentTask: (task: AgentTask) => Promise<void>;
  onAppendAgentTaskInput: (task: AgentTask, content: string, idempotencyKey: string, attachments?: AttachmentRef[]) => Promise<void>;
  onResumeAgentTask: (task: AgentTask) => Promise<void>;
  onRetryAgentTask: (task: AgentTask) => Promise<void>;
  onOpenTaskResult: (artifactID: string) => void;
  onRunAction: (action: AvailableAction) => Promise<void>;
  onCompleteScriptEdit: (pending: PendingScriptEdit) => Promise<void>;
  onResolveRevisionTarget: (resolution: TargetResolution, candidateID: string) => Promise<void>;
  onExecuteRevision: (revision: RevisionRequest) => Promise<void>;
  onPreviewRevision: (revision: RevisionRequest) => void;
  onAcceptRevision: (revision: RevisionRequest) => Promise<void>;
  onRejectRevision: (revision: RevisionRequest) => Promise<void>;
  onCancelRevision: (revision: RevisionRequest) => Promise<void>;
};

function AgentPanel({ project, goal, projectAssets, messages, agentTurns, agentTasks, agentToolCalls, agentTurnLive, activities, approvals, approvalRegistry, taskRegistry, interactionRegistry, proposedActions, revisions, resolutions, capabilities, runSnapshot, viewedRunID, streamState, composerContext, onClearSelection, onSend, onCancelAgentTurn, onControlAgentTurnPause, onAppendAgentTurnInput, onEditQueuedAgentTurn, onResolveApproval, onResolveAgentToolApproval, onConfigureAction, onStartRun, onStartAgentTask, onCancelAgentTask, onPauseAgentTask, onAppendAgentTaskInput, onResumeAgentTask, onRetryAgentTask, onOpenTaskResult, onRunAction, onCompleteScriptEdit, onResolveRevisionTarget, onExecuteRevision, onPreviewRevision, onAcceptRevision, onRejectRevision, onCancelRevision }: AgentPanelProps) {
  const principal = useContext(PrincipalContext);
  const scriptEditRecovery = (snapshot: RunSnapshot) => {
    const pending = snapshot.pending_script_edit;
    return pending && pending.project_id === project.project_id && pending.run_id === snapshot.run.run_id && pending.step_run_id === snapshot.run.current_step_run_id && snapshot.run.status === "waiting_approval"
      ? <ScriptEditRecovery pending={pending} readOnly={principal?.role === "viewer"} onComplete={onCompleteScriptEdit} /> : null;
  };
  const [activeView, setActiveView] = useState<"conversation" | "progress">("conversation");
  const [materialRequest, setMaterialRequest] = useState<{ id: string; asset: Asset } | null>(null);
  const [showEarlierMessages, setShowEarlierMessages] = useState(false);
  const timelineRef = useRef<HTMLDivElement>(null);
  const followLatestRef = useRef(true);
  const activeAgentTurns = agentTurns.filter((turn) => ["accepted", "running", "waiting_approval", "pausing", "paused", "cancel_requested", "committing"].includes(turn.status));
  const activeAgentTasks = agentTasks.filter((task) => ["queued", "running", "waiting_approval", "pausing"].includes(task.status));
  const hasAgentWork = activeAgentTurns.some((turn) => turn.status !== "paused") || activeAgentTasks.length > 0;
  const hasPausedAgent = activeAgentTurns.some((turn) => turn.status === "paused") || agentTasks.some((task) => task.status === "paused");
  const pendingAgentToolCalls = agentToolCalls.filter((call) => call.approval?.status === "pending");
  const hasAgentToolCards = agentToolCalls.length > 0;
  const empty = !runSnapshot && messages.length === 0 && activeAgentTurns.length === 0 && agentTasks.length === 0 && activities.length === 0 && approvals.length === 0 && proposedActions.length === 0 && revisions.length === 0 && !hasAgentToolCards;
  const runIDs = [...new Set(activities.map((item) => item.run_id).filter(Boolean))];
  const viewedCapabilityID = activities.find((item) => item.run_id === viewedRunID && item.capability_id)?.capability_id;
  const capabilityNames = useMemo(() => Object.fromEntries(capabilities.map((entry) => [entry.capability_id, entry.label])), [capabilities]);
  const hiddenMessageCount = Math.max(0, messages.length - 12);
  const presentedMessages = useMemo(() => presentConversationMessages(messages), [messages]);
  const visibleMessages = showEarlierMessages ? presentedMessages : presentedMessages.slice(-12);
  const activeRunMatchesView = Boolean(runSnapshot && (!viewedRunID || runSnapshot.run.run_id === viewedRunID));
  const agentStatus = streamState === "reconnecting" ? "正在重连" : pendingAgentToolCalls.length ? "等待工具授权" : hasAgentWork ? "处理中" : runSnapshot ? runStatusLabel(runSnapshot.run.status) : hasPausedAgent ? "已暂停" : "已就绪";
  const visibleProposedActions = useMemo(() => {
    const firstVisibleMessageAt = visibleMessages[0]?.created_at;
    return proposedActions.filter((action) =>
      (action.status === "consumed" || action.status === "pending") &&
      !(action.action_type === "start_background_task" && action.status === "consumed") &&
      (action.status === "pending" || showEarlierMessages || !firstVisibleMessageAt || action.created_at >= firstVisibleMessageAt),
    );
  }, [proposedActions, showEarlierMessages, visibleMessages]);
  const visibleAgentTasks = useMemo(() => {
    const firstVisibleMessageAt = visibleMessages[0]?.created_at;
    return agentTasks.filter((task) => showEarlierMessages || !firstVisibleMessageAt || task.created_at >= firstVisibleMessageAt || ["queued", "running", "waiting_approval", "pausing", "paused"].includes(task.status));
  }, [agentTasks, showEarlierMessages, visibleMessages]);
  const visibleAgentToolCalls = useMemo(() => {
    const firstVisibleMessageAt = visibleMessages[0]?.created_at;
    return agentToolCalls.filter((call) => Boolean(call.approval) && (
      call.approval?.status === "pending" || showEarlierMessages || !firstVisibleMessageAt || call.requested_at >= firstVisibleMessageAt
    ));
  }, [agentToolCalls, showEarlierMessages, visibleMessages]);
  const agentToolInteraction = useMemo(() => resolveAgentToolInteraction(interactionRegistry), [interactionRegistry]);
  const runMismatch = Boolean(runSnapshot && viewedRunID && !activeRunMatchesView);
  const recentTerminalRevisions = new Set(revisions.filter((item) => ["accepted", "rejected", "cancelled"].includes(item.status)).slice(-12).map((item) => item.revision_request_id));
  const visibleRevisions = revisions.filter((item) => !["accepted", "rejected", "cancelled"].includes(item.status) || showEarlierMessages || recentTerminalRevisions.has(item.revision_request_id) && (!visibleMessages[0]?.created_at || item.updated_at >= visibleMessages[0].created_at));
  const timeline = buildConversationTimeline({
    messages: visibleMessages,
    revisions: visibleRevisions,
    proposedActions: visibleProposedActions,
    agentTasks: visibleAgentTasks,
    agentToolCalls: visibleAgentToolCalls,
    approvals,
    runSnapshot,
    activities,
  });
  const timelineKey = timeline.map((item) => {
    if (item.kind === "revision") return `${item.kind}:${item.id}:${item.value.status}:${item.value.version}`;
    if (item.kind === "approval") return `${item.kind}:${item.id}:${item.value.status}:${item.value.version}`;
    if (item.kind === "agent_task") return `${item.kind}:${item.id}:${item.value.status}:${item.value.progress_current}:${item.value.progress_total}`;
    if (item.kind === "agent_tool_call") return `${item.kind}:${item.id}:${item.value.status}:${item.value.approval?.status ?? ""}:${item.value.approval?.version ?? 0}`;
    return `${item.kind}:${item.id}`;
  }).join("|") + "|" + activeAgentTurns.map((turn) => `${turn.agent_turn_id}:${turn.status}:${agentTurnLive[turn.agent_turn_id]?.status ?? ""}`).join("|");
  useEffect(() => {
    if (activeView !== "conversation" || !followLatestRef.current) return;
    const element = timelineRef.current;
    if (element) element.scrollTop = element.scrollHeight;
  }, [activeView, timelineKey]);
  return <aside className="agent-panel">
    <div className="agent-head">
      <div><span className={`presence ${runSnapshot?.run.status === "running" || hasAgentWork ? "running" : ""}`} /><strong>Agent</strong><span className={`agent-state ${streamState === "reconnecting" ? "stream-warning" : ""}`}>{agentStatus}</span></div>
      <div className="agent-view-tabs" role="tablist" aria-label="Agent 视图">
        <button type="button" role="tab" aria-selected={activeView === "conversation"} onClick={() => setActiveView("conversation")}><MessageSquare size={13} />对话</button>
        <button type="button" role="tab" aria-selected={activeView === "progress"} onClick={() => setActiveView("progress")}><History size={13} />进度</button>
      </div>
    </div>
    <div ref={timelineRef} className="agent-timeline" onScroll={(event) => {
      const element = event.currentTarget;
      followLatestRef.current = element.scrollHeight - element.scrollTop - element.clientHeight < 80;
    }}>
      <ProjectGoalSummary project={project} goal={goal} />
      {empty ? <div className="agent-welcome"><Sparkles size={19} /><h2>准备开始</h2><p>你可以直接描述目标。Agent 会判断是否需要引用 Skill，并在不确定时向你询问。</p></div>
        : activeView === "conversation" ? <>
          {runMismatch && <div className="agent-run-note pinned"><Clock3 size={14} /><span>另一次生成正在进行；当前显示的是左侧选中的历史产物。</span></div>}
          {runIDs.length > 1 && viewedRunID && <div className="agent-run-context"><span>当前查看</span><strong>{capabilityLabel(capabilities, viewedCapabilityID, "历史生成")}</strong><small>对话属于整个作品；当前产物和进度跟随左侧生成记录。</small></div>}
          {hiddenMessageCount > 0 && !showEarlierMessages && <button type="button" className="earlier-messages" onClick={() => setShowEarlierMessages(true)}>显示更早的 {hiddenMessageCount} 条消息</button>}
          {timeline.map((item) => {
            if (item.kind === "message") return <TimelineMessage key={`message:${item.id}`} message={item.value} assets={projectAssets} capabilityNames={capabilityNames} />;
            if (item.kind === "revision") return <RevisionCard key={`revision:${item.id}`} revision={item.value} readOnly={principal?.role === "viewer"} resolution={resolutions.find((entry) => entry.target_resolution_id === item.value.target_resolution_id) ?? null} onResolveTarget={onResolveRevisionTarget} onExecute={onExecuteRevision} onPreview={onPreviewRevision} onAccept={onAcceptRevision} onReject={onRejectRevision} onCancel={onCancelRevision} />;
            if (item.kind === "proposed_action") return <RunConfigurationCard key={`action:${item.id}`} project={project} projectAssets={projectAssets} action={item.value} capability={capabilityEntry(capabilities, item.value.capability_ref.capability_id)} onConfigured={onConfigureAction} onStart={onStartRun} onStartAgentTask={onStartAgentTask} />;
            if (item.kind === "agent_task") return <AgentTaskCard key={`agent-task:${item.id}`} task={item.value} viewKey={resolveTaskViewKey(taskRegistry, item.value.status)} capabilityName={capabilityLabel(capabilities, item.value.capability_id, "后台 Skill")} onCancel={onCancelAgentTask} onPause={onPauseAgentTask} onAppend={principal && principal.role !== "viewer" && principal.user_id === item.value.user_id ? onAppendAgentTaskInput : undefined} onResume={onResumeAgentTask} onRetry={onRetryAgentTask} onOpenResult={onOpenTaskResult} />;
            if (item.kind === "agent_tool_call") return <AgentToolApprovalCard key={`agent-tool:${item.id}`} call={item.value} capabilityNames={capabilityNames} interaction={agentToolInteraction} onResolve={onResolveAgentToolApproval} />;
            if (item.kind === "run") return <div key={`run:${item.id}`}><RunEvent snapshot={item.value} capabilities={capabilities} readOnly={principal?.role === "viewer"} onRunAction={onRunAction} />{scriptEditRecovery(item.value)}<StatefulRunInputs projectID={project.project_id} snapshot={item.value} /></div>;
            if (item.kind === "approval") return <ApprovalCard key={`approval:${item.id}`} approval={item.value} viewKey={resolveApprovalViewKey(approvalRegistry, item.value)} blockingRevision={revisionBlocksApproval(visibleRevisions, item.value)} readOnly={!principal || principal.role === "viewer"} onResolve={onResolveApproval} />;
            return null;
          })}
        </> : <>
          {runMismatch && <div className="agent-run-note pinned"><Clock3 size={14} /><span>另一次生成正在运行；以下是当前所选生成记录的进度。</span></div>}
          {runSnapshot && <><RunEvent snapshot={runSnapshot} capabilities={capabilities} readOnly={principal?.role === "viewer"} onRunAction={onRunAction} />{scriptEditRecovery(runSnapshot)}<StatefulRunInputs key={runSnapshot.run.run_id} projectID={project.project_id} snapshot={runSnapshot} /></>}
          {visibleAgentToolCalls.map((call) => <AgentToolApprovalCard key={call.agent_tool_call_id} call={call} capabilityNames={capabilityNames} interaction={agentToolInteraction} onResolve={onResolveAgentToolApproval} />)}
          {agentTasks.map((task) => <AgentTaskCard key={task.agent_task_id} task={task} viewKey={resolveTaskViewKey(taskRegistry, task.status)} capabilityName={capabilityLabel(capabilities, task.capability_id, "后台 Skill")} onCancel={onCancelAgentTask} onPause={onPauseAgentTask} onAppend={principal && principal.role !== "viewer" && principal.user_id === task.user_id ? onAppendAgentTaskInput : undefined} onResume={onResumeAgentTask} onRetry={onRetryAgentTask} onOpenResult={onOpenTaskResult} />)}
          <AgentExecutionHistory turns={agentTurns} calls={agentToolCalls} assets={projectAssets} capabilityNames={capabilityNames} onUseAsset={(asset) => { setMaterialRequest({ id: createUUID(), asset }); setActiveView("conversation"); }} />
          {activities.length > 0 ? <ProcessHistory activities={activities} activeRunID={viewedRunID} capabilityNames={capabilityNames} /> : agentTasks.length === 0 && agentTurns.length === 0 && agentToolCalls.length === 0 ? <div className="agent-welcome compact"><History size={18} /><h2>暂无运行记录</h2></div> : null}
        </>}
      <div hidden={activeView !== "conversation"} style={{ display: activeView === "conversation" ? "contents" : "none" }}>
        {agentTurns.map((turn) => <AgentTurnStatus key={turn.agent_turn_id} turn={turn} live={agentTurnLive[turn.agent_turn_id]} onCancel={onCancelAgentTurn} onControl={principal && principal.role !== "viewer" && principal.user_id === turn.user_id ? onControlAgentTurnPause : undefined} onAppend={principal && principal.role !== "viewer" && principal.user_id === turn.user_id ? onAppendAgentTurnInput : undefined} onEdit={principal && principal.role !== "viewer" && principal.user_id === turn.user_id ? onEditQueuedAgentTurn : undefined} />)}
      </div>
    </div>
    <Composer projectID={project.project_id} submissionOwner={principal ? { workspace_id: principal.workspace_id, user_id: principal.user_id, project_id: project.project_id, conversation_id: project.primary_conversation_id } : undefined} readOnly={principal?.role === "viewer"} projectAssets={projectAssets} capabilities={capabilities} runSnapshot={runSnapshot} context={composerContext} onClearSelection={onClearSelection} onSend={onSend} queueMode={activeAgentTurns.some((turn) => turn.conversation_id === project.primary_conversation_id)} materialRequest={materialRequest} onMaterialHandled={() => setMaterialRequest(null)} />
  </aside>;
}

function agentToolSummary(value: unknown): string {
  if (value == null) return "";
  if (typeof value === "string") return value;
  try {
    const serialized = JSON.stringify(value, null, 2);
    return serialized.length > 2_000 ? `${serialized.slice(0, 2_000)}\n...` : serialized;
  } catch {
    return "参数摘要不可读取";
  }
}

type AgentToolApprovalCardProps = {
  call: AgentToolCall;
  capabilityNames?: Record<string, string>;
  interaction: { viewKey: AgentToolViewKey; commands: string[] };
  readOnly?: boolean;
  onResolve: (approval: AgentToolApproval, action: "approve" | "reject") => Promise<void>;
};

export function AgentToolApprovalCard(props: AgentToolApprovalCardProps) {
  const principal = useContext(PrincipalContext);
  const readOnly = props.readOnly || principal?.role === "viewer";
  const { call } = props;
  const approval = call.approval;
  const identity = JSON.stringify([call.project_id, call.agent_tool_call_id, call.arguments_hash, approval?.agent_tool_approval_id, approval?.version, approval?.status, approval?.subject_snapshot_hash, readOnly]);
  if (call.tool_id === "runtime:update_saved_instructions") return <InstructionChangeApproval key={identity} {...props} readOnly={readOnly} />;
  if (call.tool_id === "runtime:publish_agent_memory") return <MemoryChangeApproval key={identity} {...props} userID={principal?.user_id} readOnly={readOnly} />;
  if (call.arguments_summary && typeof call.arguments_summary === "object" && !Array.isArray(call.arguments_summary) &&
      "private_memory_tool" in call.arguments_summary && call.arguments_summary.private_memory_tool === true) {
    return <MemoryToolApproval key={identity} {...props} userID={principal?.user_id} readOnly={readOnly} />;
  }
  return <AgentToolApprovalForm key={identity} {...props} readOnly={readOnly} />;
}

function AgentToolApprovalForm({ call, capabilityNames = {}, interaction, readOnly, onResolve }: AgentToolApprovalCardProps) {
  const [busy, setBusy] = useState<"approve" | "reject" | "">("");
  const [pendingAction, setPendingAction] = useState<"approve" | "reject" | "">("");
  const [failure, setFailure] = useState("");
  const submitting = useRef(false);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const approval = call.approval;
  if (!approval) return null;
  const pending = approval.status === "pending";
  const canApprove = pending && !readOnly && interaction.viewKey === "agent_tool_approval" && interaction.commands.includes("approve") && approval.options.includes("approve");
  const canReject = pending && !readOnly && interaction.viewKey === "agent_tool_approval" && interaction.commands.includes("deny") && approval.options.includes("reject");
  const status = call.status === "completed" ? "调用完成"
    : call.status === "failed" ? "调用失败"
      : call.status === "cancelled" || approval.status === "cancelled" ? "已取消"
        : approval.status === "rejected" ? "已拒绝"
          : approval.status === "approved" ? "已授权，等待执行" : "等待授权";
  const accessLabel = call.access_mode === "write" ? "写入操作" : call.access_mode === "sensitive" ? "敏感操作" : "读取操作";
  const summary = agentToolSummary(call.arguments_summary);
  const resolve = async (action: "approve" | "reject") => {
    if (submitting.current || (pendingAction && pendingAction !== action) || (action === "approve" ? !canApprove : !canReject)) return;
    submitting.current = true;
    setBusy(action);
    setPendingAction(action);
    setFailure("");
    try { await onResolve(approval, action); }
    catch (error) {
      if (!mounted.current) return;
      setFailure(errorText(error));
      if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") setPendingAction("");
    }
    finally { submitting.current = false; if (mounted.current) setBusy(""); }
  };
  return <section className={`approval-card agent-tool-approval-card ${pending ? "pending" : approval.status}`} aria-live="polite">
    <div className="approval-icon">{pending ? <Clock3 size={16} /> : approval.status === "approved" ? <ClipboardCheck size={16} /> : <CircleAlert size={16} />}</div>
    <div>
      <span className="card-kicker">{accessLabel} · {status}</span>
      <h3>{approval.title || `允许调用 ${call.tool_name}`}</h3>
      <p>{approval.reason}</p>
      <dl className="agent-tool-facts">
        <div><dt>工具</dt><dd>{call.tool_name}</dd></div>
        <div><dt>来源</dt><dd>{call.tool_kind === "mcp" ? call.server_id ? `MCP · ${call.server_id}` : "MCP" : call.tool_kind || "平台工具"}</dd></div>
        {call.execution && <div><dt>执行归属</dt><dd>{executionLabel(call.execution, capabilityNames)}</dd></div>}
      </dl>
      {call.execution && <AgentToolExecutionFacts execution={call.execution} />}
      {summary && <details className="agent-tool-arguments"><summary>查看已脱敏参数</summary><pre>{summary}</pre></details>}
      {pending && interaction.viewKey === "inspector" && <p className="form-error">当前客户端未注册可信的工具审批视图，已阻止提交操作。</p>}
      {pending && interaction.viewKey !== "inspector" && !canApprove && !canReject && <p className="form-error">服务端未开放可执行的审批命令，当前仅可查看。</p>}
      {failure && <p className="form-error" role="alert">{failure}</p>}
      {(canApprove || canReject) && <div className="approval-actions">
        {canReject && <button type="button" className="secondary-button" disabled={Boolean(busy) || pendingAction === "approve"} onClick={() => void resolve("reject")}>{busy === "reject" ? "正在拒绝…" : "拒绝调用"}</button>}
        {canApprove && <button type="button" className="primary-button" disabled={Boolean(busy) || pendingAction === "reject"} onClick={() => void resolve("approve")}>{busy === "approve" ? "正在授权…" : "允许调用"}</button>}
      </div>}
    </div>
  </section>;
}

type AgentTurnStatusProps = { turn: AgentTurn; live?: AgentTurnLiveState; readOnly?: boolean; onCancel: (turn: AgentTurn) => Promise<void>; onEdit?: (turn: AgentTurn, content: string) => Promise<void>; onControl?: (turn: AgentTurn, action: "pause" | "resume", idempotencyKey: string) => Promise<void>; onAppend?: (turn: AgentTurn, content: string, idempotencyKey: string, attachments?: AttachmentRef[]) => Promise<void> };

export function AgentTurnStatus(props: AgentTurnStatusProps) {
  const principal = useContext(PrincipalContext);
  const readOnly = props.readOnly || principal?.role === "viewer";
  const author = !principal || principal.user_id === props.turn.user_id;
  return <AgentTurnStatusForm key={`${props.turn.project_id}:${props.turn.agent_turn_id}`} {...props} readOnly={readOnly} onEdit={!readOnly && author ? props.onEdit : undefined} onControl={!readOnly && author ? props.onControl : undefined} onAppend={!readOnly && author ? props.onAppend : undefined} />;
}

function AgentTurnStatusForm({ turn, live, readOnly, onCancel, onEdit, onControl, onAppend }: AgentTurnStatusProps) {
  const [busy, setBusy] = useState(false);
  const [pending, setPending] = useState("");
  const [failure, setFailure] = useState("");
  const [original, setOriginal] = useState<AgentTurn | null>(null);
  const [draft, setDraft] = useState("");
  const pendingControl = useRef<{ action: "pause" | "resume"; key: string } | null>(null);
  const submitting = useRef(false);
  const mounted = useRef(true);
  const identity = JSON.stringify([turn.status, turn.started_at, turn.updated_at, turn.request.content, readOnly]);
  const latest = useRef({ identity, turn });
  latest.current = { identity, turn };
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => { setFailure(""); setPending(""); pendingControl.current = null; }, [identity]);
  const unavailable = (action: string) => readOnly || busy || Boolean(pending && pending !== action);
  const editable = Boolean(onEdit) && turn.status === "accepted" && !turn.started_at;
  const changed = original !== null && (!editable || original.request.content !== turn.request.content);
  const labels: Record<AgentTurn["status"], string> = {
    accepted: turn.started_at ? "等待恢复执行" : "消息排队中", running: "Agent 正在处理", waiting_approval: "等待工具授权", cancel_requested: "正在停止",
    committing: "正在保存结果", committed: "已完成", failed: "处理失败", cancelled: "已停止",
    pausing: "等待当前轮完成后暂停", paused: executionRecovery(turn.error_code)?.label ?? "本轮已暂停",
  };
  const cancellable = !readOnly && ["accepted", "running", "waiting_approval", "pausing", "paused"].includes(turn.status);
  const pauseAction = ["accepted", "running", "waiting_approval", "pausing"].includes(turn.status) ? "pause" : turn.status === "paused" ? "resume" : null;
  const pauseLabel = turn.status === "pausing" ? "保持暂停" : pauseAction === "pause" ? "暂停本轮" : "继续本轮";
  const submit = async (action: string, command: () => Promise<void>) => {
    if (submitting.current || unavailable(action)) return;
    submitting.current = true;
    setBusy(true); setPending(action); setFailure("");
    try {
      await command();
      if (mounted.current && latest.current.identity === identity) setPending("");
    } catch (error) {
      if (!mounted.current || latest.current.identity !== identity) return;
      setFailure(errorText(error));
      if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") { setPending(""); pendingControl.current = null; }
    } finally { submitting.current = false; if (mounted.current) setBusy(false); }
  };
  const control = async () => {
    if (!onControl || !pauseAction || submitting.current || unavailable(pauseAction)) return;
    if (pendingControl.current?.action !== pauseAction) pendingControl.current = { action: pauseAction, key: createUUID() };
    const key = pendingControl.current.key;
    await submit(pauseAction, async () => { await onControl(turn, pauseAction, key); if (mounted.current && latest.current.identity === identity) pendingControl.current = null; });
  };
  const cancel = async () => {
    if (!cancellable) return;
    await submit("cancel", () => onCancel(turn));
  };
  const save = async () => {
    if (!original || !onEdit || changed) return;
    await submit("edit", async () => {
      await onEdit(original, draft);
      if (mounted.current && (latest.current.identity === identity || latest.current.turn.request.content === draft.trim())) setOriginal(null);
    });
  };
  return <>{(!["committed", "failed", "cancelled"].includes(turn.status) || original) && <section className={`agent-turn-status ${turn.status}`} aria-live="polite">
    {turn.status === "paused" ? <Pause size={15} /> : turn.status === "waiting_approval" || turn.status === "accepted" ? <Clock3 size={15} /> : <LoaderCircle size={15} />}
    <div><strong>{labels[turn.status]}</strong>{["accepted", "paused", "pausing"].includes(turn.status) && <p className="queued-message-preview">{turn.request.content}</p>}{live?.status && !["accepted", "paused", "pausing"].includes(turn.status) && <small>{live.status}</small>}{live?.output && <p>{live.output}</p>}
      {original && <div className="queued-message-editor">
        <textarea aria-label="编辑排队消息内容" autoFocus value={draft} disabled={Boolean(readOnly || busy || pending)} onChange={(event) => setDraft(event.target.value)} />
        {changed && <span className="form-error" role="alert">消息已变化或开始执行，未覆盖你的草稿。</span>}
        <div className="queued-message-actions">
          <button type="button" className="icon-button" title="放弃修改" aria-label="放弃修改" disabled={busy || Boolean(pending)} onClick={() => { setOriginal(null); setFailure(""); }}><X size={16} /></button>
          <button type="button" className="icon-button" title="保存排队消息" aria-label="保存排队消息" disabled={unavailable("edit") || changed || (!draft.trim() && !original.request.attachment_refs?.length)} onClick={() => void save()}>{busy ? <LoaderCircle size={16} /> : <Check size={16} />}</button>
        </div>
      </div>}
      {failure && <span className="form-error" role="alert">{failure}</span>}
      {turn.status === "paused" && executionRecovery(turn.error_code) && <p role="status">{executionRecovery(turn.error_code)?.message}</p>}
    </div>
    <div className="queued-message-controls">
      {onControl && pauseAction && <button type="button" className="icon-button ghost" aria-label={pauseLabel} title={pauseLabel} disabled={unavailable(pauseAction)} onClick={() => void control()}>{pauseAction === "pause" ? <Pause size={15} /> : <Play size={15} />}</button>}
      {editable && !original && <button type="button" className="icon-button ghost" aria-label="编辑排队消息" title="编辑排队消息" disabled={unavailable("edit")} onClick={() => { setOriginal(turn); setDraft(turn.request.content); setFailure(""); }}><Pencil size={15} /></button>}
      {cancellable && <button type="button" className="icon-button ghost" aria-label={turn.status === "accepted" && !turn.started_at ? "取消排队消息" : "停止本轮"} title={turn.status === "accepted" && !turn.started_at ? "取消排队消息" : "停止本轮"} disabled={unavailable("cancel")} onClick={() => void cancel()}><Square size={12} fill="currentColor" /></button>}
    </div>
  </section>}<AgentTurnInputs key={turn.agent_turn_id} turn={turn} onAppend={onAppend} /></>;
}

function agentTaskResultSummary(result: unknown): string {
  if (typeof result === "string") return result;
  if (!result || typeof result !== "object" || Array.isArray(result)) return "";
  const value = result as Record<string, unknown>;
  for (const key of ["summary", "message", "content", "text"]) {
    if (typeof value[key] === "string") return value[key] as string;
  }
  return "";
}

type AgentTaskCardProps = { task: AgentTask; viewKey: TaskViewKey; capabilityName: string; readOnly?: boolean; onCancel: (task: AgentTask) => Promise<void>; onPause?: (task: AgentTask) => Promise<void>; onAppend?: (task: AgentTask, content: string, idempotencyKey: string, attachments?: AttachmentRef[]) => Promise<void>; onResume?: (task: AgentTask) => Promise<void>; onRetry: (task: AgentTask) => Promise<void>; onOpenResult: (artifactID: string) => void };

export function AgentTaskCard(props: AgentTaskCardProps) {
  const principal = useContext(PrincipalContext);
  const readOnly = props.readOnly === true || principal?.role === "viewer";
  const { task } = props;
  const identity = JSON.stringify([task.project_id, task.agent_task_id]);
  return <AgentTaskCardForm key={identity} {...props} readOnly={readOnly} />;
}

function AgentTaskCardForm({ task, viewKey, capabilityName, readOnly = false, onCancel, onPause, onAppend, onResume, onRetry, onOpenResult }: AgentTaskCardProps) {
  const [busy, setBusy] = useState("");
  const [failure, setFailure] = useState("");
  const [pending, setPending] = useState("");
  const submitting = useRef(false);
  const mounted = useRef(true);
  const executionIdentity = JSON.stringify([task.status, task.attempt_count, task.cancel_requested, task.input_pause_requested, readOnly]);
  const latestExecution = useRef(executionIdentity);
  latestExecution.current = executionIdentity;
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => { setPending(""); setFailure(""); }, [executionIdentity]);
  const disabled = (key: string) => readOnly || Boolean(busy || pending && pending !== key);
  const perform = async (key: string, operation: () => Promise<void>) => {
    if (submitting.current || disabled(key)) return;
    submitting.current = true; setPending(key);
    setBusy(key); setFailure("");
    try { await operation(); if (mounted.current) setPending(""); }
    catch (error) {
      if (mounted.current && latestExecution.current === executionIdentity) {
        setFailure(errorText(error));
        if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") setPending("");
      }
    }
    finally { submitting.current = false; if (mounted.current) setBusy(""); }
  };
  const statusLabel: Record<string, string> = {
    queued: "等待执行", running: task.cancel_requested ? "正在取消" : "执行中", waiting_approval: "等待工具授权",
    completed: "已完成", failed: "执行失败", cancelled: "已取消", pausing: "暂停中", paused: executionRecovery(task.failure_code)?.label ?? "已暂停",
  };
  const status = statusLabel[task.status] ?? "状态不可识别";
  const progressTotal = Math.max(0, task.progress_total || 0);
  const progressCurrent = Math.min(Math.max(0, task.progress_current || 0), progressTotal || 1);
  const resultSummary = agentTaskResultSummary(task.result);
  const cancellable = !readOnly && viewKey === "task_progress" && !task.cancel_requested && ["queued", "running", "waiting_approval", "pausing", "paused"].includes(task.status);
  const pausable = !readOnly && viewKey === "task_progress" && !task.cancel_requested && (["queued", "running", "waiting_approval"].includes(task.status) || (task.status === "pausing" && task.input_pause_requested));
  const resumable = !readOnly && viewKey === "task_progress" && !task.cancel_requested && task.status === "paused";
  const retryable = !readOnly && !task.retry_blocked_reason && ((viewKey === "task_failure" && task.status === "failed") || (viewKey === "task_result" && task.status === "cancelled"));
  const icon = task.status === "running" ? <LoaderCircle className="task-spinner" size={15} />
    : task.status === "completed" ? <Check size={15} />
      : task.status === "failed" ? <CircleAlert size={15} />
        : task.status === "cancelled" ? <Square size={13} /> : <Clock3 size={15} />;
  return <section className={`agent-task-card ${viewKey} ${task.status}`} aria-live={task.status === "running" ? "polite" : undefined}>
    <header><span>{icon}<strong>{capabilityName}</strong></span><small>{status}</small></header>
    <p>{(task.status === "paused" ? executionRecovery(task.failure_code)?.message : null) ?? (task.progress_message || (task.status === "queued" ? "任务已进入后台队列。" : task.status === "completed" ? "后台任务已完成。" : task.status === "failed" ? "后台任务未完成，原有产物未被覆盖。" : task.status === "cancelled" ? "后台任务已停止。" : "正在执行后台任务。"))}</p>
    {progressTotal > 0 && <div className="agent-task-progress"><progress max={progressTotal} value={progressCurrent} /><span>{progressCurrent}/{progressTotal}</span></div>}
    {resultSummary && <p className="agent-task-result">{resultSummary}</p>}
    {task.failure_message && task.status === "failed" && <p className="form-error">{task.failure_message}</p>}
    {task.retry_blocked_reason && <p className="form-error">{task.retry_blocked_reason}</p>}
    <AgentTaskInputs task={task} onAppend={!readOnly && viewKey !== "inspector" ? onAppend : undefined} />
    {viewKey === "inspector" && <p className="form-error">当前客户端不支持该任务状态的交互视图，已切换为只读。</p>}
    {failure && <p className="form-error">{failure}</p>}
    {(cancellable || retryable || task.result_artifact_id) && <div className="approval-actions">
      {pausable && onPause && <button type="button" className="icon-button" title="暂停后台任务" aria-label="暂停后台任务" disabled={disabled("pause")} onClick={() => void perform("pause", () => onPause(task))}><Pause size={15} /></button>}
      {resumable && onResume && <button type="button" className="icon-button" title="继续后台任务" aria-label="继续后台任务" disabled={disabled("resume")} onClick={() => void perform("resume", () => onResume(task))}><Play size={15} /></button>}
      {task.result_artifact_id && <button type="button" className="secondary-button" onClick={() => onOpenResult(task.result_artifact_id!)}>查看结果</button>}
      {cancellable && <button type="button" className="secondary-button" disabled={disabled("cancel")} onClick={() => void perform("cancel", () => onCancel(task))}>{busy === "cancel" ? "正在取消…" : "取消任务"}</button>}
      {retryable && <button type="button" className="primary-button" disabled={disabled("retry")} onClick={() => void perform("retry", () => onRetry(task))}>{busy === "retry" ? "正在重试…" : "重新执行"}</button>}
    </div>}
  </section>;
}

type RunConfigurationCardProps = { project: Project; projectAssets: Asset[]; action: ProposedAction; capability: ComposerRegistryEntry | null; readOnly?: boolean; onConfigured: (action: ProposedAction) => void; onStart: (action: ProposedAction) => Promise<void>; onStartAgentTask?: (action: ProposedAction) => Promise<void> };

export function RunConfigurationCard(props: RunConfigurationCardProps) {
  const principal = useContext(PrincipalContext);
  const { project, action } = props;
  const revision = canonicalJSONStringify([project.project_id, action.proposed_action_id, action.version, action.status, action.action_type, action.snapshot_hash, action.capability_ref]);
  return <RunConfigurationForm key={revision} {...props} readOnly={props.readOnly === true || principal?.role === "viewer"} />;
}

function RunConfigurationForm({ project, projectAssets, action, capability, readOnly = false, onConfigured, onStart, onStartAgentTask }: RunConfigurationCardProps) {
  const savedConfig = action.config?.payload && typeof action.config.payload === "object"
    ? action.config.payload as Record<string, unknown>
    : {};
  const savedConfigRef = useRef(savedConfig);
  const [episodes, setEpisodes] = useState(() => String(Number(savedConfig.target_episode_count) || 12));
  const [duration, setDuration] = useState(() => String(Number(savedConfig.episode_duration_minutes) || 2));
  const [targetLength, setTargetLength] = useState(() => String(Number(savedConfig.target_length_chars) || 15000));
  const [episodeExecutionMode, setEpisodeExecutionMode] = useState<"continuous" | "review_each">(() => savedConfig.episode_execution_mode === "review_each" ? "review_each" : "continuous");
  const [genericConfig, setGenericConfig] = useState(() => JSON.stringify(savedConfig, null, 2));
  const [genericConfigValue, setGenericConfigValue] = useState<Record<string, unknown>>(() => savedConfig);
  const [configRef, setConfigRef] = useState(() => String(action.config?.config_ref || capability?.config.default_ref || capability?.config.options[0] || ""));
  const [capabilityDefinition, setCapabilityDefinition] = useState<CapabilityDefinition | null>(null);
  const [capabilityDetailFailure, setCapabilityDetailFailure] = useState("");
  const [definitionLoadAttempt, setDefinitionLoadAttempt] = useState(0);
  const [busy, setBusy] = useState(false);
  const submittingRef = useRef(false);
  const mountedRef = useRef(false);
  const configurationRequestRef = useRef<{ fingerprint: string; key: string } | null>(null);
  const initializedSchemaRef = useRef("");
  const initializedConfigRef = useRef(false);
  const [failure, setFailure] = useState("");
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);
  useEffect(() => {
    let active = true;
    setCapabilityDefinition(null);
    setCapabilityDetailFailure("");
    void api.getProjectCapability(
      project.project_id, action.capability_ref.capability_id, action.capability_ref.version,
      action.proposed_action_id,
    ).then((definition) => {
      if (definition.capability_id !== action.capability_ref.capability_id || definition.version !== action.capability_ref.version) {
        throw new ApiError("CAPABILITY_VERSION_MISMATCH", "返回的 Skill 配置版本与确认卡不一致。", 409);
      }
      if (active) setCapabilityDefinition(definition);
    }).catch((error) => {
      if (active) setCapabilityDetailFailure(errorText(error));
    });
    return () => { active = false; };
  }, [project.project_id, action.proposed_action_id, action.capability_ref.capability_id, action.capability_ref.version, definitionLoadAttempt]);
  const definitionAvailable = capabilityDefinition?.status === "available" && !capabilityDetailFailure;
  const configOptions = capabilityDefinition?.config_options?.length
    ? capabilityDefinition.config_options
    : capability?.config.options ?? [];
  const defaultConfigRef = capabilityDefinition?.default_config_ref || capability?.config.default_ref || configOptions[0] || "";
  useEffect(() => {
    if (!capabilityDefinition || configOptions.length === 0) return;
    if (!initializedConfigRef.current) {
      initializedConfigRef.current = true;
      const savedRef = String(action.config?.config_ref || "");
      setConfigRef(configOptions.includes(savedRef) ? savedRef : defaultConfigRef);
      return;
    }
    if (configOptions.includes(configRef)) return;
    setConfigRef(defaultConfigRef);
  }, [action.config, capabilityDefinition, configOptions, configRef, defaultConfigRef]);
  const selectedConfigSchema = capabilityDefinition?.config_schemas?.[configRef];
  const schemaFormAvailable = Boolean(selectedConfigSchema && schemaCanUseForm(selectedConfigSchema));
  useEffect(() => {
    if (!selectedConfigSchema) return;
    const initializationKey = `${action.proposed_action_id}:${action.capability_ref.version}:${configRef}`;
    if (initializedSchemaRef.current === initializationKey) return;
    const saved = action.config?.config_ref === configRef ? savedConfigRef.current : {};
    const value = schemaDefaults(selectedConfigSchema, saved);
    setGenericConfigValue(value);
    setGenericConfig(JSON.stringify(value, null, 2));
    initializedSchemaRef.current = initializationKey;
  }, [action.capability_ref.version, action.config, action.proposed_action_id, configRef, selectedConfigSchema]);
  const rawGenericConfigValid = useMemo(() => {
    try {
      const value = JSON.parse(genericConfig || "{}");
      return Boolean(value && typeof value === "object" && !Array.isArray(value));
    } catch { return false; }
  }, [genericConfig]);
  const schemaConfigValid = Boolean(selectedConfigSchema && (
    schemaFormAvailable ? schemaValueIsValid(selectedConfigSchema, genericConfigValue) : rawGenericConfigValid
  ));
  const label = capabilityDefinition?.label ?? capability?.label ?? "当前能力";
  const configView = trustedConfigViewKey(capabilityDefinition?.ui_entry?.config_view_key ?? capability?.config.view_key ?? "inspector");
  const continuation = configView === "script_continuation";
  const acceptedAssetKinds = capabilityDefinition?.accepted_asset_kinds ?? capability?.accepted_asset_kinds ?? [];
  const compatibleAssets = projectAssets.filter((asset) =>
    asset.status === "available" && asset.parse_status === "completed" &&
    acceptedAssetKinds.includes(asset.kind),
  );
  const boundAssets = Array.isArray(action.input.assets)
    ? action.input.assets as Array<{ asset_id: string; asset_snapshot_id: string; role: string; order: number }>
    : [];
  const boundArtifactVersions = Array.isArray(action.input.artifact_versions)
    ? action.input.artifact_versions as Array<{ artifact_version_id: string; role: string; order: number }>
    : [];
  const [selectedAssetID, setSelectedAssetID] = useState(() =>
    boundAssets[0]?.asset_id ?? (compatibleAssets.length === 1 ? compatibleAssets[0].asset_id : ""),
  );
  const selectedAsset = compatibleAssets.find((asset) => asset.asset_id === selectedAssetID);
  const hasMaterial = boundAssets.length > 0 || boundArtifactVersions.length > 0 || Boolean(selectedAsset);
  const requiresMaterial = (configView !== "json_schema" && configView !== "none") || !capabilityDefinition?.input_schema || schemaRequiresMaterial(capabilityDefinition.input_schema);
  const lengthSchema = selectedConfigSchema?.properties?.target_length_chars;
  const minLength = lengthSchema?.minimum ?? 1000;
  const maxLength = lengthSchema?.maximum ?? 50000;
  const targetLengthChars = Number(targetLength);
  const validTargetLength = targetLength.trim() !== "" && Number.isInteger(targetLengthChars) && targetLengthChars >= minLength && targetLengthChars <= maxLength;
  const episodeCount = Number(episodes);
  const episodeDuration = Number(duration);
  const validEpisodeCount = episodes.trim() !== "" && Number.isInteger(episodeCount) && episodeCount >= 1 && episodeCount <= 200;
  const validEpisodeDuration = duration.trim() !== "" && episodeDuration >= 0.5 && episodeDuration <= 10;
  const configure = async () => {
    if (readOnly || submittingRef.current || !definitionAvailable || action.status !== "pending" || action.action_type !== "collect_run_configuration") return;
    submittingRef.current = true;
    setBusy(true); setFailure("");
    try {
      const inputBinding = capabilityDefinition?.input_binding ?? capability?.input_binding;
      const sourceType = inputBinding?.source_type;
      if (!sourceType) throw new ApiError("CAPABILITY_INPUT_BINDING_MISSING", "该 Skill 没有可用的输入绑定，请在 Skill 管理中检查 manifest。", 422);
      const role = inputBinding?.asset_role || "primary_source";
      const assets = boundAssets.length > 0 ? boundAssets : selectedAsset ? [{ asset_id: selectedAsset.asset_id, asset_snapshot_id: selectedAsset.current_snapshot_id, role, order: 1 }] : [];
      const input: Record<string, unknown> = { ...action.input, project_id: project.project_id, source_type: sourceType };
      if (assets.length > 0) input.assets = assets;
      else delete input.assets;
      let config: { config_ref: string; payload: Record<string, unknown> };
      if (configView === "script_continuation") {
        config = { config_ref: defaultConfigRef || "continuation", payload: { target_length_chars: targetLengthChars } };
      } else if (configView === "script_generation") {
        config = { config_ref: defaultConfigRef || "creation", payload: { target_episode_count: episodeCount, episode_duration_minutes: episodeDuration, episode_execution_mode: episodeExecutionMode, preserve_existing_episode_marks: true, expansion_policy: "confirm_if_needed", user_requirements: [] } };
      } else if (configView === "json_schema") {
        if (!selectedConfigSchema) throw new Error("CONFIG_SCHEMA_UNAVAILABLE");
        const payload = schemaFormAvailable ? genericConfigValue : JSON.parse(genericConfig || "{}") as unknown;
        if (!payload || typeof payload !== "object" || Array.isArray(payload)) throw new Error("CONFIG_OBJECT_REQUIRED");
        if (!schemaConfigValid) throw new Error("CONFIG_SCHEMA_INVALID");
        if (!configRef) throw new Error("CONFIG_REF_REQUIRED");
        config = { config_ref: configRef, payload: payload as Record<string, unknown> };
      } else {
        config = { config_ref: defaultConfigRef || "default", payload: {} };
      }
      const fingerprint = canonicalJSONStringify({ project_id: project.project_id, proposed_action_id: action.proposed_action_id, expected_version: action.version, input, config });
      if (configurationRequestRef.current?.fingerprint !== fingerprint) {
        configurationRequestRef.current = { fingerprint, key: createUUID() };
      }
      const configured = await api.configureProposedAction(action, input, config, configurationRequestRef.current.key);
      if (configured.proposed_action_id !== action.proposed_action_id || configured.version !== action.version + 1 || configured.status !== "pending" || configured.capability_ref?.capability_id !== action.capability_ref.capability_id || configured.capability_ref.version !== action.capability_ref.version || configured.project_id && configured.project_id !== project.project_id || configured.conversation_id && configured.conversation_id !== project.primary_conversation_id) throw new ApiError("CONFIGURATION_RECEIPT_INVALID", "配置回执与当前确认不一致，请刷新确认。", 502);
      if (mountedRef.current) onConfigured(configured);
    } catch (error) { if (mountedRef.current) setFailure(error instanceof SyntaxError || (error instanceof Error && error.message.startsWith("CONFIG_")) ? "配置内容不完整或格式无效，请检查必填项。" : errorText(error)); }
    finally { submittingRef.current = false; if (mountedRef.current) setBusy(false); }
  };
  const start = async () => {
    if (readOnly || submittingRef.current || !definitionAvailable || action.status !== "pending" || !["start_run", "start_background_task"].includes(action.action_type)) return;
    submittingRef.current = true;
    setBusy(true); setFailure("");
    try {
      if (action.action_type === "start_background_task") {
        if (!onStartAgentTask) throw new ApiError("ACTION_UNSUPPORTED", "当前客户端不能启动该后台任务。", 400);
        await onStartAgentTask(action);
      } else {
        await onStart(action);
      }
    }
    catch (error) { if (mountedRef.current) setFailure(errorText(error)); }
    finally { submittingRef.current = false; if (mountedRef.current) setBusy(false); }
  };
  const consumed = action.status === "consumed";
  if (readOnly) return <section className="config-card completed"><span className="card-kicker">{consumed ? "已确认" : "只读"}</span><h3>{label}</h3></section>;
  const definitionNotice = !consumed && !definitionAvailable && <div aria-live="polite">
    <p className={capabilityDetailFailure || capabilityDefinition ? "form-error" : "bound-material"}>{capabilityDetailFailure || (capabilityDefinition ? "当前 Skill 不可用。" : "正在读取 Skill 配置…")}</p>
    {(capabilityDetailFailure || capabilityDefinition) && <button type="button" className="icon-button" aria-label="重试读取配置" title="重试读取配置" onClick={() => setDefinitionLoadAttempt((value) => value + 1)}><RefreshCw size={16} /></button>}
  </div>;
  if (!consumed && action.status !== "pending") {
    const statusLabel = ({ superseded: "已被新请求替代", expired: "已过期", cancelled: "已取消" } as Record<string, string>)[action.status] ?? "已失效";
    return <section className="config-card completed"><span className="card-kicker">{statusLabel}</span><h3>{label}</h3></section>;
  }
  if (!capability || configView === "inspector") {
    return <section className="config-card inspector-card"><span className="card-kicker">只读 Inspector</span><h3>{label}</h3><p>当前客户端不信任该 Skill 声明的配置视图，已阻止启动。请在 Skill 管理中检查版本和 manifest。</p><dl><div><dt>Capability</dt><dd>{action.capability_ref.capability_id}</dd></div><div><dt>配置视图</dt><dd>{capability?.config.view_key ?? "missing"}</dd></div></dl></section>;
  }
  if (action.action_type === "start_background_task") {
    return <section className={`config-card background-task-confirmation ${consumed ? "completed" : ""}`}>
      <span className="card-kicker">{consumed ? "已确认 · 已加入后台" : "需要确认"}</span>
      <h3>{consumed ? `${label}已进入后台任务` : `确认启动${label}`}</h3>
      <p>{consumed ? "任务状态和结果会保留在当前作品中。" : "任务会在后台执行；离开当前页面不会中断，完成后结果会保存到当前作品。"}</p>
      {definitionNotice}
      {failure && <p className="form-error" aria-live="polite">{failure}</p>}
      {!consumed && <div className="approval-actions"><button className="primary-button" disabled={busy || !definitionAvailable} onClick={() => void start()}>{busy ? "正在提交…" : "确认并开始"}</button></div>}
    </section>;
  }
  if (configView === "video_extraction") {
    const configured = action.action_type === "start_run";
    return <section className={`config-card ${consumed ? "completed" : ""}`}><span className="card-kicker">{consumed ? "已确认 · 已启动" : configured ? "等待启动确认" : "视频材料未就绪"}</span><h3>{consumed ? "视频解析配置" : configured ? "确认开始解析视频" : "请重新上传并确认视频批次"}</h3><p>{configured ? "按已确认顺序逐集解析；采用高还原提取、秒级时间码，并明确标注无法确认的内容。" : "当前动作没有关联已封存的视频批次，不能开始解析。"}</p>{definitionNotice}{failure && <p className="form-error" aria-live="polite">{failure}</p>}{!consumed && <div className="approval-actions">{configured && <button className="primary-button" disabled={busy || !definitionAvailable || action.input.collection_state !== "sealed"} onClick={() => void start()}>{busy ? "正在提交…" : "确认并开始"}</button>}</div>}</section>;
  }
  return <section className="config-card">
    <span className="card-kicker">{consumed ? "已确认 · 已启动" : action.action_type === "start_run" ? "等待启动确认" : label}</span>
    <h3>{consumed ? "本次生成配置" : action.action_type === "start_run" ? "确认本次生成配置" : continuation ? "确认续写剧本" : configView === "json_schema" || configView === "none" ? `配置${label}` : "设置剧本体量"}</h3>
    {action.action_type === "collect_run_configuration" ? <>
      {requiresMaterial && boundAssets.length === 0 && boundArtifactVersions.length === 0 && <label className="material-field">
        <span>本次使用的材料</span>
        <select value={selectedAssetID} disabled={busy} onChange={(event) => setSelectedAssetID(event.target.value)}>
          <option value="">{compatibleAssets.length ? "请选择作品材料" : "作品中暂无可用材料"}</option>
          {compatibleAssets.map((asset) => <option key={asset.asset_id} value={asset.asset_id}>{asset.display_name || asset.original_filename}</option>)}
        </select>
        <small>上传后的材料会随作品保留，不受对话轮次影响。</small>
      </label>}
      {(boundAssets.length > 0 || boundArtifactVersions.length > 0) && <p className="bound-material">已使用本次请求选定的材料。</p>}
      {continuation ? <div className="config-fields"><label>目标字数<input type="number" min={minLength} max={maxLength} step="1000" value={targetLength} disabled={busy} onChange={(event) => setTargetLength(event.target.value)} /></label></div> : configView === "script_generation" ? <><div className="config-fields">
        <label>目标集数<input type="number" min="1" max="200" value={episodes} disabled={busy} onChange={(event) => setEpisodes(event.target.value)} /></label>
        <label>每集时长<input type="number" min="0.5" max="10" step="0.5" value={duration} disabled={busy} onChange={(event) => setDuration(event.target.value)} /><span>分钟</span></label>
      </div>
		<fieldset className="episode-execution-choice">
			<legend>生成节奏</legend>
			<label className={episodeExecutionMode === "continuous" ? "selected" : ""}><input type="radio" name={`episode-mode-${action.proposed_action_id}`} value="continuous" checked={episodeExecutionMode === "continuous"} disabled={busy} onChange={() => setEpisodeExecutionMode("continuous")} /><span><strong>自动生成全部</strong><small>连续生成全部单集，最后统一确认</small></span></label>
			<label className={episodeExecutionMode === "review_each" ? "selected" : ""}><input type="radio" name={`episode-mode-${action.proposed_action_id}`} value="review_each" checked={episodeExecutionMode === "review_each"} disabled={busy} onChange={() => setEpisodeExecutionMode("review_each")} /><span><strong>逐集确认</strong><small>每生成一集先确认或修改，再继续下一集</small></span></label>
		</fieldset></> : configView === "json_schema" ? <div className="generic-config-form">
        <label>配置项<select value={configRef} disabled={busy || !definitionAvailable} onChange={(event) => setConfigRef(event.target.value)}>{configOptions.map((option) => <option key={option} value={option}>{option}</option>)}</select></label>
        {selectedConfigSchema && schemaFormAvailable && <JsonSchemaForm schema={selectedConfigSchema} value={genericConfigValue} onChange={(value) => {
          setGenericConfigValue(value);
          setGenericConfig(JSON.stringify(value, null, 2));
        }} disabled={busy} />}
        {selectedConfigSchema && !schemaFormAvailable && <label>高级配置 JSON<textarea className="raw-schema-config" value={genericConfig} disabled={busy} onChange={(event) => setGenericConfig(event.target.value)} spellCheck={false} /></label>}
      </div> : <p className="bound-material">该 Skill 不需要额外配置。</p>}
    </> : continuation ? <p>目标 {targetLengthChars.toLocaleString("zh-CN")} 字；先选择续写方向，再生成正文。</p> : configView === "script_generation" ? <p>共 {episodes} 集，每集约 {duration} 分钟。{episodeExecutionMode === "review_each" ? "每集生成后等待确认，再继续下一集。" : "将连续生成全部单集，最后统一确认。"}</p> : <p>配置已保存，可以开始运行。</p>}
    {failure && <p className="form-error" aria-live="polite">{failure}</p>}
    {definitionNotice}
    {!consumed && <div className="approval-actions"><button className="primary-button" disabled={busy || !definitionAvailable || (continuation && !validTargetLength) || (configView === "script_generation" && (!validEpisodeCount || !validEpisodeDuration)) || (action.action_type === "collect_run_configuration" && configView === "json_schema" && (!configRef || !capabilityDefinition || Boolean(capabilityDetailFailure) || !schemaConfigValid)) || (action.action_type === "collect_run_configuration" && requiresMaterial && !hasMaterial)} onClick={() => void (action.action_type === "start_run" ? start() : configure())}>{busy ? "正在提交…" : action.action_type === "start_run" ? "确认并开始" : continuation ? "保存并生成方向" : "保存配置"}</button></div>}
  </section>;
}

type RevisionCardProps = { revision: RevisionRequest; resolution: TargetResolution | null; readOnly?: boolean; onResolveTarget: (resolution: TargetResolution, candidateID: string) => Promise<void>; onExecute: (revision: RevisionRequest) => Promise<void>; onPreview: (revision: RevisionRequest) => void; onAccept: (revision: RevisionRequest) => Promise<void>; onReject: (revision: RevisionRequest) => Promise<void>; onCancel: (revision: RevisionRequest) => Promise<void> };

export function RevisionCard(props: RevisionCardProps) {
  const revision = props.revision;
  const identity = JSON.stringify([revision.project_id, revision.revision_request_id, revision.version, revision.status, revision.artifact_id, revision.base_artifact_version_id, props.readOnly, props.resolution?.target_resolution_id, props.resolution?.status, props.resolution?.candidates.map((item) => [item.candidate_id, item.artifact_id, item.artifact_version_id])]);
  return <RevisionCardForm key={identity} {...props} />;
}

function RevisionCardForm({ revision, resolution, readOnly = false, onResolveTarget, onExecute, onPreview, onAccept, onReject, onCancel }: RevisionCardProps) {
  const [busy, setBusy] = useState("");
  const [failure, setFailure] = useState("");
  const [pending, setPending] = useState("");
  const submitting = useRef(false);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const blocked = (key: string) => readOnly || Boolean(busy) || Boolean(pending && pending !== key);
  const perform = async (key: string, action: () => Promise<void>) => {
    if (blocked(key) || submitting.current) return;
    submitting.current = true; setBusy(key); setPending(key); setFailure("");
    try { await action(); if (mounted.current) setPending(""); }
    catch (error) {
      if (mounted.current) {
        setFailure(errorText(error));
        if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") setPending("");
      }
    } finally { submitting.current = false; if (mounted.current) setBusy(""); }
  };
  const label = resolution?.display?.artifact_label ?? resolution?.candidates?.[0]?.display?.artifact_label ?? "目标产物";
  const location = resolution?.display?.location_label;
  if (revision.status === "waiting_target_confirmation" && (!resolution || resolution.target_resolution_id !== revision.target_resolution_id || resolution.project_id !== revision.project_id || resolution.conversation_id !== revision.conversation_id || resolution.request_message_id !== revision.request_message_id || resolution.status !== "ambiguous")) return <section className="revision-card target-card"><h3>修改位置待核对</h3><p className="form-error" role="alert">定位结果与当前修改请求不一致，请刷新。</p>{failure && <p className="form-error" aria-live="polite">{failure}</p>}{!readOnly && <button className="text-action danger-text" disabled={blocked("cancel")} onClick={() => void perform("cancel", () => onCancel(revision))}>取消这次修改</button>}</section>;
  if (revision.status === "waiting_target_confirmation" && resolution) return <section className="revision-card target-card"><span className="card-kicker">需要确认修改位置</span><h3>你指的是哪一项？</h3><p className="revision-instruction">{revision.instruction}</p><div className="target-options">{resolution.candidates.map((candidate) => <button type="button" disabled={blocked(candidate.candidate_id)} key={candidate.candidate_id} onClick={() => void perform(candidate.candidate_id, () => onResolveTarget(resolution, candidate.candidate_id))}><span><strong>{candidate.display.artifact_label ?? candidate.artifact_type}</strong><small>{candidate.display.location_label || "整个产物"}</small></span>{busy === candidate.candidate_id ? <span className="mini-spinner" /> : <ArrowUp size={14} />}</button>)}</div>{failure && <p className="form-error" aria-live="polite">{failure}</p>}{!readOnly && <button className="text-action danger-text" disabled={blocked("cancel")} onClick={() => void perform("cancel", () => onCancel(revision))}>取消这次修改</button>}</section>;
  const authorizationFailed = revision.status === "failed" && ["REVISION_EXECUTION_OWNER_REQUIRED", "REVISION_EXECUTION_OWNER_INVALID", "AUTHENTICATION_REQUIRED", "WORKSPACE_ACCESS_DENIED", "ROLE_FORBIDDEN", "PROJECT_NOT_FOUND", "IDENTITY_INVALID"].includes(revision.failure_code ?? "");
  const statusCopy: Record<string, string> = { waiting_safe_checkpoint: "已排队，将在当前步骤完成后的安全检查点处理。", queued: "修改请求已就绪，可以生成修改稿。", running: "Agent 正在根据已锁定的上下文生成修改稿。", failed: authorizationFailed ? "修改执行授权不可用，原版本未受影响。" : "修改稿生成失败，原版本未受影响。", stale: "目标产物已有新版本，需要重新提出修改要求。", accepted: "修改已保存为新版本。", rejected: "修改稿已放弃。", cancelled: "修改请求已取消。" };
  const heading = revision.status === "proposed" ? "修改稿已生成" : revision.status === "running" ? "正在生成修改稿" : revision.status === "accepted" ? "修改稿已采用" : revision.status === "rejected" ? "修改稿已放弃" : revision.status === "cancelled" ? "修改请求已取消" : "修改请求已记录";
  return <section className={`revision-card ${revision.status}`}>
    <span className="card-kicker">Agent 修改 · {label}{location ? ` · ${location}` : ""}</span><h3>{heading}</h3>
    <p className="revision-instruction">{revision.instruction}</p>{revision.proposal_summary && <p>{revision.proposal_summary}</p>}
    {statusCopy[revision.status] && <p className="revision-status-copy">{statusCopy[revision.status]}</p>}
    {failure && <p className="form-error" role="alert">{failure}</p>}
    <div className="approval-actions">
      {!readOnly && ["queued", "failed"].includes(revision.status) && <button className="primary-button" disabled={blocked("execute")} onClick={() => void perform("execute", () => onExecute(revision))}>{busy === "execute" ? "正在提交…" : authorizationFailed ? "确认并重新执行" : revision.status === "failed" ? "重新生成修改稿" : "生成修改稿"}</button>}
      {revision.status === "proposed" && <>
        <button className="secondary-button" disabled={Boolean(busy)} onClick={() => onPreview(revision)}>查看修改稿</button>
        {!readOnly && <><button className="secondary-button" disabled={blocked("reject")} onClick={() => void perform("reject", () => onReject(revision))}>放弃修改稿</button><button className="primary-button" disabled={blocked("accept")} onClick={() => void perform("accept", () => onAccept(revision))}>{busy === "accept" ? "正在应用…" : "确认采用"}</button></>}
      </>}
      {!readOnly && ["waiting_safe_checkpoint", "queued", "failed"].includes(revision.status) && <button className="secondary-button" disabled={blocked("cancel")} onClick={() => void perform("cancel", () => onCancel(revision))}>取消请求</button>}
    </div>
  </section>;
}

function TaskFailure({ task, retryAvailable }: { task: TaskItem; retryAvailable: boolean }) {
  const detail = task.failure_detail;
  if (!detail) return <small title={task.failure ?? undefined}>{retryAvailable ? "处理失败，可重试" : "处理失败"}</small>;
  const diagnostics = [
    detail.technical_detail,
    detail.provider_status_code ? `Provider 状态：${detail.provider_status_code}` : null,
    detail.provider_request_id ? `请求 ID：${detail.provider_request_id}` : null,
    detail.transport_category ? `传输类型：${detail.transport_category}` : null,
  ].filter(Boolean) as string[];
  return <div className="task-failure">
    <small>{detail.summary}{retryAvailable ? " 可重试。" : ""}</small>
    {diagnostics.length > 0 && <details><summary>查看技术详情</summary>{diagnostics.map((item) => <p key={item}>{item}</p>)}</details>}
    {detail.provider_output && <details className="provider-output"><summary>查看模型原始返回</summary><pre>{detail.provider_output}</pre></details>}
  </div>;
}

type RunEventProps = { snapshot: RunSnapshot; capabilities?: ComposerRegistryEntry[]; readOnly?: boolean; onRunAction: (action: AvailableAction) => Promise<void> };

export function RunEvent(props: RunEventProps) {
  const { snapshot, readOnly } = props;
  const identity = JSON.stringify([snapshot.run.run_id, snapshot.run.current_step_run_id, snapshot.run.status, readOnly,
    snapshot.available_actions.map((action) => [runActionIdentity(action), action.enabled]),
    snapshot.run.status === "failed" ? snapshot.task_items?.map((task) => [task.task_item_id, task.status, task.attempt_count, task.current_attempt_id]) : null]);
  return <RunEventForm key={identity} {...props} />;
}

function RunEventForm({ snapshot, capabilities = [], readOnly = false, onRunAction }: RunEventProps) {
  const [busyAction, setBusyAction] = useState("");
  const submitting = useRef(false);
  const mounted = useRef(true);
  const [pendingAction, setPendingAction] = useState("");
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const [cancelPending, setCancelPending] = useState<AvailableAction | null>(null);
  const [moreOpen, setMoreOpen] = useState(false);
  const [partialContinuePending, setPartialContinuePending] = useState<AvailableAction | null>(null);
  const [failure, setFailure] = useState("");
  useEffect(() => {
    if (!moreOpen) return;
    const close = (event: PointerEvent) => {
      if (!(event.target as Element).closest(".run-more")) setMoreOpen(false);
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape") setMoreOpen(false);
    };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", escape);
    return () => {
      document.removeEventListener("pointerdown", close);
      document.removeEventListener("keydown", escape);
    };
  }, [moreOpen]);
  const tasks = currentStepTasks(snapshot.task_items ?? [], snapshot.run.current_step_run_id);
  const completed = tasks.filter(taskIsCompleted).length;
  const failed = tasks.filter((task) => task.status === "failed").length;
  const retrying = tasks.find(taskIsRetrying);
  const retryAvailable = snapshot.available_actions.some((action) => runActionMatchesSnapshot(snapshot, action) && action.action_id === "retry_failed_step");
  const currentStep = snapshot.steps?.find((step) => step.step_run_id === snapshot.run.current_step_run_id);
  const stepLabel = stepLabels[currentStep?.step_id ?? ""] ?? "准备下一步骤";
  const runCapabilityLabel = capabilityLabel(capabilities, snapshot.run.capability_id, "内容生成");
  const config = snapshot.run.config_snapshot?.payload;
  const configSummary = config?.target_episode_count && config?.episode_duration_minutes
    ? `${config.target_episode_count} 集 · 每集约 ${config.episode_duration_minutes} 分钟`
    : null;
  const instruction = snapshot.current_approval
    ? `请完成“${snapshot.current_approval.title}”确认，确认后流程会继续。`
    : snapshot.run.status === "running"
      ? retrying
        ? `上一次处理未通过输出校验，正在进行第 ${retrying.attempt_count} 次自动重试，无需重复操作。`
        : "当前无需操作。步骤完成后，Agent 会展示产物和下一项确认。"
      : snapshot.run.status === "paused"
        ? tasks.filter((task) => task.status === "paused").map((task) => executionRecovery(task.failure)?.message).find(Boolean) ?? "生成已暂停，可在当前任务卡中继续。"
        : snapshot.run.status === "pausing"
          ? "正在完成当前处理项；不会再开始下一项。也可以撤销暂停并继续执行。"
        : snapshot.run.status === "failed"
          ? retryAvailable
            ? "已完成内容仍然保留，可在当前任务卡中重试失败步骤。"
            : "已完成内容仍然保留；当前失败无法直接重试，请结束本次运行后重新发起。"
          : snapshot.run.status === "completed"
            ? "本次生成已完成，可以查看和编辑左侧产物。"
            : "运行状态已更新。";
	const controls = snapshot.available_actions.filter((action) => {
		if (readOnly) return false;
		if (!runActionMatchesSnapshot(snapshot, action) || !["pause_run", "resume_run", "retry_failed_step", "continue_with_partial_results"].includes(action.action_id)) return false;
		return action.action_id !== "pause_run" || snapshot.run.status === "running";
	});
	const cancelAction = readOnly ? null : snapshot.available_actions.find((action) => runActionMatchesSnapshot(snapshot, action) && action.action_id === "cancel_run") ?? null;
  const actionDisabled = (action: AvailableAction) => Boolean(busyAction || pendingAction && pendingAction !== runActionIdentity(action));
  const perform = async (action: AvailableAction) => {
    if (readOnly || submitting.current || actionDisabled(action) || !runActionMatchesSnapshot(snapshot, action)) throw new ApiError("RUN_STATE_CONFLICT", "当前运行操作不可提交。", 409);
    submitting.current = true;
    setPendingAction(runActionIdentity(action));
    setBusyAction(action.action_id); setFailure("");
    try { await onRunAction(action); if (mounted.current) setPendingAction(""); }
    catch (error) {
      if (mounted.current) {
        if (!["cancel_run", "continue_with_partial_results"].includes(action.action_id)) setFailure(errorText(error));
        if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408) setPendingAction("");
      }
      throw error;
    }
    finally { submitting.current = false; if (mounted.current) setBusyAction(""); }
  };
  return <><section className={`run-event ${snapshot.run.status}`}><span className="event-line" /><div>
    <span className="run-event-kicker">{runCapabilityLabel}{configSummary ? ` · ${configSummary}` : ""}</span>
    <strong>{snapshot.current_approval ? "等待你的确认" : `${runStatusLabel(snapshot.run.status)} · ${stepLabel}`}</strong><p>{instruction}</p>
    {tasks.length > 0 && <div className="task-progress"><header><span>{taskProgressTitle(tasks)}</span><strong>{completed}/{tasks.length} 完成{failed ? ` · ${failed} 失败` : retrying ? " · 正在重试" : ""}</strong></header><div className="task-list">{tasks.map((task) => { const processing = task.status === "running" || taskIsRetrying(task); return <div key={task.task_item_id}><span>{taskProgressLabel(task)}</span><strong className={`task-status ${taskIsRetrying(task) ? "retrying" : task.status}`}>{processing && <LoaderCircle className="task-spinner" size={13} aria-hidden="true" />}{taskRuntimeStatusLabel(task)}</strong>{task.status === "failed" && task.failure && <TaskFailure task={task} retryAvailable={retryAvailable} />}</div>; })}</div></div>}
    {failure && <p className="form-error" aria-live="polite">{failure}</p>}
    {(controls.length > 0 || cancelAction) && <div className="run-controls"><div className="run-primary-controls">{controls.map((action) => <button type="button" key={action.action_id} className={action.action_id === "continue_with_partial_results" ? "primary-button" : "secondary-button"} disabled={actionDisabled(action)} onClick={() => action.action_id === "continue_with_partial_results" ? setPartialContinuePending(action) : void perform(action).catch(() => {})}>{busyAction === action.action_id ? <><LoaderCircle className="task-spinner" size={13} aria-hidden="true" />处理中…</> : action.action_id === "pause_run" ? <><Pause size={13} aria-hidden="true" />暂停任务</> : action.action_id === "resume_run" ? <><Play size={13} aria-hidden="true" />{snapshot.run.status === "pausing" ? "撤销暂停" : "继续任务"}</> : action.action_id === "retry_failed_step" ? "重试失败项" : "按现有结果继续"}</button>)}</div>
      {cancelAction && <div className="run-more"><button type="button" className="icon-button ghost" aria-label="更多运行操作" title="更多运行操作" aria-expanded={moreOpen} disabled={actionDisabled(cancelAction)} onClick={() => setMoreOpen((open) => !open)}><MoreHorizontal size={16} /></button>{moreOpen && <div className="run-more-menu" role="menu"><button type="button" role="menuitem" disabled={actionDisabled(cancelAction)} onClick={() => { setMoreOpen(false); setCancelPending(cancelAction); }}><Square size={12} aria-hidden="true" />结束本次运行</button></div>}</div>}
    </div>}
  </div></section>
    {cancelPending && <CancelRunDialog action={cancelPending} onCancel={() => setCancelPending(null)} onConfirm={async () => { await perform(cancelPending); if (mounted.current) setCancelPending(null); }} />}
    {partialContinuePending && <PartialContinueDialog action={partialContinuePending} tasks={tasks} onCancel={() => setPartialContinuePending(null)} onConfirm={async () => { await perform(partialContinuePending); if (mounted.current) setPartialContinuePending(null); }} />}
  </>;
}

type ApprovalCardProps = {
  approval: Approval;
  viewKey: ApprovalViewKey;
  blockingRevision: RevisionRequest | null;
  readOnly?: boolean;
  onResolve: (approval: Approval, action: string, instruction?: string, resolutionPayload?: unknown) => Promise<void>;
};

export function ApprovalCard(props: ApprovalCardProps) {
  const { approval, viewKey } = props;
  const identity = JSON.stringify([approval.approval_request_id, approval.run_id, approval.version, approval.status,
    approval.subject_kind, approval.subject_ref_id, approval.subject_version, approval.subject_snapshot_hash, approval.options, viewKey]);
  return <ApprovalCardForm key={identity} {...props} />;
}

function ApprovalCardForm({ approval, viewKey, blockingRevision, readOnly = false, onResolve }: ApprovalCardProps) {
  const [busy, setBusy] = useState<string | null>(null);
  const submitting = useRef(false);
  const mounted = useRef(true);
  const [failure, setFailure] = useState("");
  const [readFailure, setReadFailure] = useState("");
  const [reload, setReload] = useState(0);
  const needsSubject = ["quality_review", "adaptation_strategy", "single_option"].includes(viewKey);
  const [loadingSubject, setLoadingSubject] = useState(needsSubject);
  const [qualityReview, setQualityReview] = useState<QualityReview | null>(null);
  const [revisionAction, setRevisionAction] = useState<"request_ai_revision" | "ai_revise" | "confirm_change" | null>(null);
  const [instruction, setInstruction] = useState("");
  const [revisionTargets, setRevisionTargets] = useState<ApprovalRevisionTarget[]>([]);
  const [revisionTargetID, setRevisionTargetID] = useState("");
  const [targetFailure, setTargetFailure] = useState("");
  const [loadingTargets, setLoadingTargets] = useState(false);
  const [targetReload, setTargetReload] = useState(0);
  const [revisionPending, setRevisionPending] = useState(false);
  const needsRevisionTargets = revisionAction === "request_ai_revision" && approval.subject_kind === "artifact_version_set";
  const [confirmRegeneration, setConfirmRegeneration] = useState(false);
  const [adaptationOptions, setAdaptationOptions] = useState<AdaptationOptions | null>(null);
  const [selectedOptionIDs, setSelectedOptionIDs] = useState<string[]>([]);
  const [continuationOptions, setContinuationOptions] = useState<ContinuationOptions | null>(null);
  const [selectedContinuationOptionID, setSelectedContinuationOptionID] = useState("");
  const [targetEpisodes, setTargetEpisodes] = useState("12");
  const [episodeDuration, setEpisodeDuration] = useState("2");
  const [customChanges, setCustomChanges] = useState("");
  const [expansionStrategy, setExpansionStrategy] = useState("只补充因果桥段和必要过渡，不新增关键设定和主线。");
  const blocked = readOnly || approval.status !== "pending" || Boolean(blockingRevision);
  const unavailable = blocked || busy !== null || loadingSubject || Boolean(readFailure);
  useEffect(() => {
    if (!needsRevisionTargets || readOnly || approval.status !== "pending" || revisionPending) return;
    let active = true;
    setLoadingTargets(true); setTargetFailure(""); setRevisionTargets([]); setRevisionTargetID("");
    void api.getApprovalRevisionTargets(approval.approval_request_id).then((response) => {
      if (!active) return;
      const bound = response?.approval;
      if (!bound || bound.approval_request_id !== approval.approval_request_id || bound.project_id !== approval.project_id ||
          bound.run_id !== approval.run_id || bound.status !== "pending" || bound.version !== approval.version ||
          bound.subject_kind !== approval.subject_kind || bound.subject_ref_id !== approval.subject_ref_id ||
          bound.subject_version !== approval.subject_version || bound.subject_snapshot_hash !== approval.subject_snapshot_hash ||
          !Array.isArray(response.targets) || !response.targets.length) {
        throw new ApiError("APPROVAL_SUBJECT_CHANGED", "修改对象与当前审批版本不一致，请刷新核对。", 409);
      }
      const seen = new Set<string>();
      for (const target of response.targets) {
        if (!target || typeof target.artifact_id !== "string" || !target.artifact_id.trim() ||
            typeof target.artifact_version_id !== "string" || !target.artifact_version_id.trim() ||
            typeof target.artifact_type !== "string" || !target.artifact_type.trim() || typeof target.scope_key !== "string" ||
            !Number.isSafeInteger(target.version) || target.version < 1 || typeof target.label !== "string" || !target.label.trim() ||
            seen.has(target.artifact_version_id)) throw new ApiError("TARGET_CANDIDATE_INVALID", "修改对象列表无效，请重新读取。", 422);
        seen.add(target.artifact_version_id);
      }
      setRevisionTargets(response.targets);
      if (response.targets.length === 1) setRevisionTargetID(response.targets[0].artifact_version_id);
    }).catch((error) => { if (active) setTargetFailure(errorText(error)); })
      .finally(() => { if (active) setLoadingTargets(false); });
    return () => { active = false; };
  }, [needsRevisionTargets, approval.approval_request_id, approval.project_id, approval.run_id, approval.status,
    approval.version, approval.subject_kind, approval.subject_ref_id, approval.subject_version,
    approval.subject_snapshot_hash, readOnly, targetReload, revisionPending]);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => {
    if (!needsSubject || approval.status !== "pending" || readOnly) return;
    let active = true;
    setLoadingSubject(true); setReadFailure("");
    setQualityReview(null); setAdaptationOptions(null); setContinuationOptions(null);
    setSelectedOptionIDs([]); setSelectedContinuationOptionID("");
    const load = async () => {
      if (viewKey === "quality_review") {
        const review = await api.getQualityReview(approval.subject_ref_id);
        if (!active) return;
        if (review.quality_review_id !== approval.subject_ref_id || review.review_version !== approval.subject_version ||
            review.input_snapshot_hash !== approval.subject_snapshot_hash || (approval.run_id && review.run_id !== approval.run_id) ||
            !review.issue_counts || Object.values(review.issue_counts).length !== 4 || Object.values(review.issue_counts).some((count) => !Number.isSafeInteger(count) || count < 0) || !Array.isArray(review.affected_episode_nos)) {
          throw new ApiError("APPROVAL_SUBJECT_CHANGED", "审核内容与当前确认版本不符。", 409);
        }
        setQualityReview(review);
        return;
      }
      const version = await api.getArtifactVersion(approval.subject_ref_id);
      if (!active) return;
      if (version.artifact_version_id !== approval.subject_ref_id || version.version !== approval.subject_version) {
        throw new ApiError("APPROVAL_SUBJECT_CHANGED", "候选内容与当前确认版本不符。", 409);
      }
      const value = version.payload;
      if (!validApprovalOptions(value, viewKey === "adaptation_strategy")) {
        throw new ApiError("APPROVAL_SUBJECT_INVALID", "候选内容无法读取，请重试或刷新当前作品。", 422);
      }
      if (viewKey === "adaptation_strategy") {
        setAdaptationOptions(value as AdaptationOptions);
        setSelectedOptionIDs([(value as AdaptationOptions).options[0].option_id]);
      } else {
        setContinuationOptions(value as ContinuationOptions);
      }
    };
    void load().catch((error) => { if (active) setReadFailure(errorText(error)); })
      .finally(() => { if (active) setLoadingSubject(false); });
    return () => { active = false; };
  }, [needsSubject, viewKey, approval.subject_ref_id, approval.subject_version, approval.status, readOnly, reload]);
  const allowed = (action: string) => approval.options.includes(action);
  const resolve = async (action: string, detail = "", resolutionPayload?: unknown) => {
    if (submitting.current || unavailable || !allowed(action)) return;
    submitting.current = true; setBusy(action); setFailure("");
    if (action === "request_ai_revision") setRevisionPending(true);
    try { await onResolve(approval, action, detail, resolutionPayload); if (mounted.current) setRevisionPending(false); }
    catch (error) {
      if (mounted.current) {
        setFailure(errorText(error));
        if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") setRevisionPending(false);
      }
    }
    finally { submitting.current = false; if (mounted.current) setBusy(null); }
  };
  const labels: Record<string, string> = { approve: approval.scope === "episode_checkpoint" ? "确认并生成下一集" : "确认并继续", approve_all_remaining: "确认并自动生成剩余集", edit_artifact: "手动编辑", request_ai_revision: approval.scope === "episode_checkpoint" ? "修改本集" : "让 Agent 修改", regenerate_artifact: "重新生成", revise: "继续修改", select_adaptation_strategy: "确认改编方案", select_single_option: "确认选择", select_final: "确认设为最终稿", accept_with_risk: "确认保留风险", ai_revise: "让 Agent 返修", manual_edit: "手动修改", confirm_change: "确认修改" };
  const visibleOptions = approval.scope === "episode_checkpoint"
    ? [...approval.options.filter((option) => option !== "edit_artifact"), ...(allowed("approve") ? ["approve_all_remaining"] : [])]
    : viewKey !== "quality_review" ? approval.options : qualityReview ? approval.options.filter((option) => {
      if (option === "accept_with_risk") return qualityReview.issue_counts.blocker === 0;
      if (option === "confirm_change") return qualityReview.recommended_route === "user";
      if (option === "ai_revise") return qualityReview.recommended_route !== "user";
      return true;
    }) : [];
  const choose = (option: string) => {
    if (unavailable || submitting.current) return;
    if (option === "approve_all_remaining" && allowed("approve")) { void resolve("approve", "", { episode_execution_mode: "continuous" }); return; }
    if (!allowed(option)) return;
    if (option === "request_ai_revision" || option === "ai_revise" || option === "confirm_change") { setRevisionAction(option); setConfirmRegeneration(false); return; }
    if (option === "regenerate_artifact") { setConfirmRegeneration(true); setRevisionAction(null); return; }
    void resolve(option);
  };
  const validTargetEpisodes = targetEpisodes.trim() !== "" && Number.isInteger(Number(targetEpisodes)) && Number(targetEpisodes) >= 1 && Number(targetEpisodes) <= 200;
  const validEpisodeDuration = episodeDuration.trim() !== "" && Number(episodeDuration) >= 0.5 && Number(episodeDuration) <= 10;
  const confirmAdaptation = () => {
    if (!validTargetEpisodes || !validEpisodeDuration || !selectedOptionIDs.length) return;
    const requirements = customChanges.split(/\r?\n/).map((item) => item.trim()).filter(Boolean);
    void resolve("select_adaptation_strategy", "", {
      adaptation_options_artifact_version_id: approval.subject_ref_id,
      selection: { selected_option_ids: selectedOptionIDs, combined_methods: selectedOptionIDs.length > 1 ? selectedOptionIDs : [], custom_changes: requirements },
      creation_config: { target_episode_count: Number(targetEpisodes), episode_duration_minutes: Number(episodeDuration), user_requirements: requirements },
    });
  };
  if (viewKey === "inspector" || readOnly || approval.status !== "pending") return <section className="approval-card inspector-card">
    <div className="approval-icon"><CircleAlert size={16} /></div><div><span className="card-kicker">只读确认记录</span><h3>{approval.title}</h3><p>{approval.reason}</p>
      <small>{viewKey === "inspector" ? "当前客户端未注册该审批视图，已阻止提交操作。" : readOnly ? "当前账号无编辑权限。" : "该确认请求已不处于待处理状态。"}</small></div>
  </section>;
  return <section className={`approval-card ${blocked ? "blocked" : ""}`}>
    <div className="approval-icon"><Clock3 size={16} /></div><div>
      <span className="card-kicker">等待确认 · 版本 {approval.subject_version}</span><h3>{approval.title}</h3><p>{approval.reason}</p>
      {blockingRevision && <p className="approval-blocked-note">“{blockingRevision.instruction}”的修改稿{blockingRevision.status === "proposed" ? "等待处理" : "正在生成"}。完成采用或取消修改后，才能确认当前版本。</p>}
      {qualityReview && <QualityReviewSummary review={qualityReview} />}
      {loadingSubject ? <p className="approval-loading" role="status">正在读取确认内容…</p>
        : readFailure ? <div><p className="form-error" role="alert">{readFailure}</p><button type="button" className="secondary-button" onClick={() => setReload((value) => value + 1)}><RefreshCw size={14} />重试读取确认内容</button></div>
        : revisionAction ? <div className="approval-detail">
          {needsRevisionTargets && <div className="approval-revision-target">
            {loadingTargets ? <p role="status">正在读取修改对象…</p> : targetFailure ? <div><p className="form-error" role="alert">{targetFailure}</p><button type="button" className="secondary-button" disabled={blocked || revisionPending} onClick={() => setTargetReload((value) => value + 1)}><RefreshCw size={14} />重试读取修改对象</button></div>
              : <label>修改对象<select disabled={blocked || busy !== null || revisionPending} value={revisionTargetID} onChange={(event) => setRevisionTargetID(event.target.value)}><option value="">请选择产物</option>{revisionTargets.map((target) => <option key={target.artifact_version_id} value={target.artifact_version_id}>{target.label}</option>)}</select></label>}
          </div>}
          <label>{revisionAction === "confirm_change" ? "确认后的故事变更" : "修改要求"}<textarea autoFocus disabled={blocked || busy !== null || revisionPending} value={instruction} onChange={(event) => setInstruction(event.target.value)} placeholder={revisionAction === "confirm_change" ? "明确确认采用的新设定或故事变化" : "说明需要保留、调整或重点加强的内容"} /></label>
          <div className="approval-actions"><button className="secondary-button" disabled={busy !== null || revisionPending} onClick={() => { setRevisionAction(null); setInstruction(""); }}>取消</button>
            <button className="primary-button" disabled={unavailable || !instruction.trim() || (needsRevisionTargets && (loadingTargets || Boolean(targetFailure) || !revisionTargets.some((target) => target.artifact_version_id === revisionTargetID)))} onClick={() => void resolve(revisionAction, instruction.trim(), needsRevisionTargets ? { target_artifact_version_id: revisionTargetID } : undefined)}>{busy === revisionAction ? "正在提交…" : revisionAction === "confirm_change" ? "确认变更并返工" : "提交修改要求"}</button></div>
        </div> : confirmRegeneration ? <div className="approval-detail">
          <p>将保留当前版本记录，并从当前步骤重新生成；受影响的后续产物会进入待重生成状态。</p>
          <div className="approval-actions"><button className="secondary-button" disabled={busy !== null} onClick={() => setConfirmRegeneration(false)}>取消</button><button className="primary-button" disabled={unavailable} onClick={() => void resolve("regenerate_artifact")}>{busy === "regenerate_artifact" ? "正在提交…" : "确认重新生成"}</button></div>
        </div> : viewKey === "single_option" && continuationOptions ? <div className="adaptation-selection continuation-selection">
          <p className="selection-guidance">{continuationOptions.selection_instructions}</p>
          <div className="adaptation-options">{continuationOptions.options.map((option) => <label key={option.option_id} className={selectedContinuationOptionID === option.option_id ? "selected" : ""}>
            <input type="radio" name={`selection-${approval.approval_request_id}`} checked={selectedContinuationOptionID === option.option_id} disabled={unavailable} onChange={() => setSelectedContinuationOptionID(option.option_id)} />
            <span><strong>{option.title}{option.genre_tag ? ` · ${option.genre_tag}` : ""}</strong>{option.summary && <small>{option.summary}</small>}{option.outline && <em>{option.outline}</em>}</span>
          </label>)}</div>
          <div className="approval-actions">{allowed("request_ai_revision") && <button className="secondary-button" disabled={unavailable} onClick={() => choose("request_ai_revision")}>让 Agent 修改候选</button>}
            {allowed("select_single_option") && <button className="primary-button" disabled={unavailable || !selectedContinuationOptionID} onClick={() => void resolve("select_single_option", "", { options_artifact_version_id: approval.subject_ref_id, selected_option_id: selectedContinuationOptionID })}>{busy === "select_single_option" ? "正在确认…" : "确认选择并继续"}</button>}</div>
        </div> : viewKey === "adaptation_strategy" && adaptationOptions ? <div className="adaptation-selection">
          <p className="selection-guidance">{adaptationOptions.selection_instructions}</p>
          <div className="adaptation-options">{adaptationOptions.options.map((option) => { const checked = selectedOptionIDs.includes(option.option_id); return <label key={option.option_id} className={checked ? "selected" : ""}>
            <input type="checkbox" checked={checked} disabled={unavailable} onChange={() => setSelectedOptionIDs((items) => checked ? items.filter((id) => id !== option.option_id) : [...items, option.option_id])} />
            <span><strong>{option.title}</strong><small>{option.one_sentence_strategy}</small>{option.risks.length > 0 && <em>风险：{option.risks.join("；")}</em>}</span></label>; })}</div>
          <div className="config-fields"><label>目标集数<input type="number" min="1" max="200" disabled={unavailable} value={targetEpisodes} onChange={(event) => setTargetEpisodes(event.target.value)} /></label><label>每集时长<input type="number" min="0.5" max="10" step="0.5" disabled={unavailable} value={episodeDuration} onChange={(event) => setEpisodeDuration(event.target.value)} /><span>分钟</span></label></div>
          <label className="adaptation-custom">补充改编要求<textarea disabled={unavailable} value={customChanges} onChange={(event) => setCustomChanges(event.target.value)} placeholder="每行一项；没有可留空" /></label>
          <div className="approval-actions">{allowed("request_ai_revision") && <button className="secondary-button" disabled={unavailable} onClick={() => choose("request_ai_revision")}>让 Agent 修改</button>}{allowed("select_adaptation_strategy") && <button className="primary-button" disabled={unavailable || !selectedOptionIDs.length || !validTargetEpisodes || !validEpisodeDuration} onClick={confirmAdaptation}>{busy === "select_adaptation_strategy" ? "正在确认…" : "确认方案并生成 Brief"}</button>}</div>
        </div> : viewKey === "volume_fit" ? <div className="approval-detail">
          <label>扩写策略<textarea disabled={unavailable} value={expansionStrategy} onChange={(event) => setExpansionStrategy(event.target.value)} /></label>
          <div className="approval-actions">{allowed("approve") && <button className="primary-button" disabled={unavailable || !expansionStrategy.trim()} onClick={() => void resolve("approve", "", { expansion_strategy: expansionStrategy.trim() })}>{busy === "approve" ? "正在确认…" : "确认策略并继续"}</button>}</div>
        </div> : <div className="approval-actions">{visibleOptions.map((option, index) => <button key={option} disabled={unavailable} className={index === 0 ? "primary-button" : "secondary-button"} onClick={() => choose(option)}>{busy === option ? "正在提交…" : labels[option] ?? "确认操作"}</button>)}</div>}
      {failure && <p className="form-error" aria-live="polite">{failure}</p>}
    </div>
  </section>;
}

function validApprovalOptions(value: unknown, adaptation: boolean): boolean {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const payload = value as Record<string, unknown>;
  if (payload.selection_instructions !== undefined && typeof payload.selection_instructions !== "string") return false;
  if (!Array.isArray(payload.options) || payload.options.length === 0) return false;
  const seen = new Set<string>();
  return payload.options.every((item: unknown) => {
    if (!item || typeof item !== "object" || Array.isArray(item)) return false;
    const option = item as Record<string, unknown>;
    if (typeof option.option_id !== "string" || !option.option_id.trim() || seen.has(option.option_id) ||
        typeof option.title !== "string" || !option.title.trim()) return false;
    seen.add(option.option_id);
    if (adaptation) return typeof option.one_sentence_strategy === "string" && Array.isArray(option.risks) && option.risks.every((risk) => typeof risk === "string");
    return ["genre_tag", "summary", "outline"].every((field) => option[field] === undefined || typeof option[field] === "string");
  });
}

function QualityReviewSummary({ review }: { review: QualityReview }) {
  const result = review.result && typeof review.result === "object" ? review.result as Record<string, unknown> : {};
  const issues = Array.isArray(result.issues) ? result.issues.filter((item): item is Record<string, unknown> => Boolean(item) && typeof item === "object" && !Array.isArray(item)) : [];
  const routeLabels: Record<string, string> = { source_analysis: "来源分析", story_bible: "故事设定", episode_plan: "分集规划", script_generation: "单集剧本", user: "等待故事变更确认" };
  return <div className="quality-summary"><div className="quality-overview"><span className={review.issue_counts.blocker ? "critical" : ""}>阻断 {review.issue_counts.blocker}</span><span>高 {review.issue_counts.high}</span><span>中 {review.issue_counts.medium}</span><span>低 {review.issue_counts.low}</span></div><p className="quality-route">总体处理目标：{routeLabels[review.recommended_route ?? ""] ?? "按审核结果返修"}{review.affected_episode_nos.length ? ` · 第 ${review.affected_episode_nos.join("、")} 集` : ""}</p><div className="quality-issues">{issues.map((issue, index) => <article key={String(issue.issue_id ?? index)}><header><strong>{String(issue.severity ?? "issue").toUpperCase()}</strong><span>{Array.isArray(issue.episode_nos) ? `第 ${issue.episode_nos.join("、")} 集` : "全剧"}</span></header><p>{String(issue.evidence ?? "未提供证据位置")}</p><small>影响：{String(issue.impact ?? "未说明")}</small><small>修改目标：{String(issue.revision_target ?? "按证据修复")}</small></article>)}</div></div>;
}

type ComposerProps = { projectID: string; submissionOwner?: MessageOwner; readOnly?: boolean; projectAssets: Asset[]; capabilities: ComposerRegistryEntry[]; runSnapshot: RunSnapshot | null; context: AgentComposerContext; onClearSelection: () => void; onSend: SendMessage; queueMode?: boolean; materialRequest?: { id: string; asset: Asset } | null; onMaterialHandled?: () => void };

export function Composer(props: ComposerProps) {
  return <ComposerForm key={canonicalJSONStringify([props.projectID, props.submissionOwner])} {...props} />;
}

function ComposerForm({ projectID, submissionOwner, readOnly = false, projectAssets, capabilities, runSnapshot, context, onClearSelection, onSend, queueMode = false, materialRequest, onMaterialHandled }: ComposerProps) {
  useModalKeyboard();
  const [recovery] = useState(() => {
    try {
      if (submissionOwner && submissionOwner.project_id !== projectID) throw new MessageStorageError("消息身份与当前作品不匹配，未读取或发送原消息。");
      return { record: submissionOwner ? messageSubmission.read(submissionOwner) : null, error: "", legacy: submissionOwner ? messageSubmission.hasLegacy(projectID) : false };
    }
    catch (error) { return { record: null, error: errorText(error), legacy: false }; }
  });
  const savedMessage = useRef<SavedMessage | null>(recovery.record);
  const settledMessage = useRef<SavedMessage | null>(null);
  const [savedPending, setSavedPending] = useState(Boolean(recovery.record));
  const [cleanupPending, setCleanupPending] = useState(false);
  const [storageFailure, setStorageFailure] = useState(recovery.error);
  const [recoveryNotice, setRecoveryNotice] = useState(recovery.legacy ? "发现缺少身份和版本信息的旧版交接记录，未自动发送，原记录仍保留。" : "");
  const starterDispatched = useRef(false);
  const mounted = useRef(false);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const [value, setValue] = useState(recovery.record?.draft.content ?? "");
  const [selected, setSelected] = useState<ComposerRegistryEntry | null>(recovery.record?.draft.capability ?? null);
  const [menu, setMenu] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const [attachments, setAttachments] = useState<AttachmentRef[]>(recovery.record?.draft.attachments ?? []);
  const [uploadNotice, setUploadNotice] = useState("");
  const [videoAttachmentIDs, setVideoAttachmentIDs] = useState<string[]>(recovery.record?.draft.attachments.filter((item) => item.kind === "video").map((item) => item.asset_id) ?? []);
  const [uploading, setUploading] = useState(false);
  const [videoBatch, setVideoBatch] = useState<AssetSetSnapshot | null>(recovery.record?.draft.video_batch ?? null);
  const [batchOpen, setBatchOpen] = useState(false);
  const [batchPending, setBatchPending] = useState(false);
  const materialBusy = useRef(false);
  const [batchPreparationFailed, setBatchPreparationFailed] = useState(false);
  const [videoPickerOpen, setVideoPickerOpen] = useState(false);
  const batchAppendCommand = useRef<{ signature: string; key: string } | null>(null);
  const retryVideoPreparation = useRef<{ attachments: AttachmentRef[]; capability: ComposerRegistryEntry | null } | null>(null);
  const [materialPickerOpen, setMaterialPickerOpen] = useState(false);
  const handledMaterial = useRef<string | null>(null);
  const responseController = useRef<AbortController | null>(null);
  const pendingSubmission = useRef<{ payload: string; key: string } | null>(null);
  const autoFilledPrompt = useRef<string | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (!menu) return;
    const close = (event: PointerEvent) => { if (!(event.target as Element).closest(".capability-control")) setMenu(false); };
    const escape = (event: KeyboardEvent) => event.key === "Escape" && setMenu(false);
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", escape);
    return () => { document.removeEventListener("pointerdown", close); document.removeEventListener("keydown", escape); };
  }, [menu]);
  const running = Boolean(runSnapshot && !["completed", "cancelled"].includes(runSnapshot.run.status));
  const reusableVideos = projectAssets.filter((asset) => asset.kind === "video" && asset.status === "available");
  const videoCapability = selected && trustedComposerViewKey(selected.view_key) === "video_asset_set"
    ? selected
    : capabilities.find((entry) => entry.status === "available" && trustedComposerViewKey(entry.view_key) === "video_asset_set" && capabilityAccepts(entry, "video")) ?? null;
  const videoSkillSelected = Boolean(selected && trustedComposerViewKey(selected.view_key) === "video_asset_set");
  const autoVideoSkillCandidate = !selected && videoAttachmentIDs.length > 0 && Boolean(videoCapability);
  const activeVideoBatch = videoSkillSelected || autoVideoSkillCandidate ? videoBatch : null;
  const submissionPayload = (content: string, capability: ComposerRegistryEntry | null, refs: AttachmentRef[], batch: AssetSetSnapshot | null) => canonicalJSONStringify({
    projectID, content, capability: capability ? { capability_id: capability.capability_id, version: capability.version } : null,
    attachments: refs, video_batch_version: batch?.version.asset_set_version_id ?? null, context,
  });
  const clearSubmittedDraft = () => {
    pendingSubmission.current = null;
    savedMessage.current = null; settledMessage.current = null;
    setSavedPending(false); setCleanupPending(false); setRecoveryNotice("");
    autoFilledPrompt.current = null;
    setValue(""); setSelected(null); setAttachments([]); setVideoAttachmentIDs([]); setVideoBatch(null);
  };
  const clearSaved = () => {
    const record = settledMessage.current;
    if (!record) return;
    try { messageSubmission.clear(record); clearSubmittedDraft(); setFailure(""); }
    catch (error) { setFailure(errorText(error)); }
  };
  const sendSaved = async (record: SavedMessage) => {
    if (readOnly || responseController.current || !mounted.current) return;
    if (record.phase === "prepared" && record.draft.capability && !capabilities.some((entry) => composerSkillIdentity(entry) === composerSkillIdentity(record.draft.capability!) && capabilityDependenciesReady(entry) && trustedComposerViewKey(entry.view_key) !== "inspector")) {
      setFailure("所选 Skill 已更新或不可用，原消息未发送。请按新请求编辑后重新选择。"); return;
    }
    const controller = new AbortController();
    const current = () => mounted.current && responseController.current === controller;
    responseController.current = controller;
    setBusy(true); setFailure(""); setMenu(false);
    try {
      const claimed = messageSubmission.claim(record);
      savedMessage.current = claimed; setSavedPending(true);
      const draft = claimed.draft;
      await onSend(draft.content, draft.capability, draft.attachments, draft.video_batch, controller.signal, claimed.key, { context: draft.context, clientInstanceID: draft.clientInstanceID, recovering: record.phase === "pending" });
      if (controller.signal.aborted) return;
      settledMessage.current = claimed;
      if (current()) setCleanupPending(true);
      messageSubmission.clear(claimed);
      if (current()) clearSubmittedDraft();
    } catch (error) {
      if (current()) { setFailure(errorText(error)); setRecoveryNotice(settledMessage.current ? "消息已提交，本地记录待清理。" : "原消息结果待确认。"); }
    } finally {
      if (current()) { responseController.current = null; setBusy(false); }
    }
  };
  const editAsNew = () => {
    if (busy || readOnly || !savedMessage.current || cleanupPending) return;
    if (!window.confirm("原请求可能已经执行。改为新请求不会撤销原操作，再次发送可能创建新一轮，确认编辑？")) return;
    try {
      messageSubmission.clear(savedMessage.current);
      savedMessage.current = null; pendingSubmission.current = null;
      setSavedPending(false); setRecoveryNotice(""); setFailure("");
    } catch (error) { setFailure(errorText(error)); }
  };
  const refreshSaved = () => {
    if (busy || !submissionOwner || submissionOwner.project_id !== projectID) return;
    try {
      const record = messageSubmission.read(submissionOwner);
      savedMessage.current = record; settledMessage.current = null; pendingSubmission.current = null;
      starterDispatched.current = true;
      setSavedPending(Boolean(record)); setCleanupPending(false); setStorageFailure(""); setFailure("");
      if (record) {
        setValue(record.draft.content); setSelected(record.draft.capability); setAttachments(record.draft.attachments); setVideoBatch(record.draft.video_batch);
        setVideoAttachmentIDs(record.draft.attachments.filter((item) => item.kind === "video").map((item) => item.asset_id));
        setRecoveryNotice("已读取原消息，结果仍待确认。");
      } else setRecoveryNotice("本地没有待发送记录，请核对对话状态。");
    } catch (error) { setStorageFailure(errorText(error)); }
  };
  const draftLocked = () => readOnly || Boolean(savedMessage.current) || Boolean(storageFailure) || Boolean(responseController.current);
  const submit = async () => {
    if (readOnly || storageFailure || cleanupPending || !value.trim() || busy || uploading || batchPending || batchPreparationFailed || materialBusy.current || responseController.current) return;
    if (savedMessage.current) { await sendSaved(savedMessage.current); return; }
    const incompatible = selected && attachments.find((item) => item.kind && item.kind !== "archive" && !item.hidden && !capabilityAccepts(selected, item.kind));
    if (incompatible) { setFailure(`${selected?.label} 不接受 ${incompatible.display_name} 这类材料。`); return; }
    if ((videoSkillSelected || activeVideoBatch) && activeVideoBatch?.asset_set.status !== "sealed") {
      setFailure("请先上传视频并确认批次，再发送解析目标。");
      return;
    }
    const content = value.trim();
    const capability = selected;
    let submittedAttachments = attachments;
    let submittedVideoBatch = activeVideoBatch;
    try {
      if (activeVideoBatch) {
        if (!videoBatchMatches(activeVideoBatch, projectID, activeVideoBatch.asset_set.asset_set_id)) throw new ApiError("ASSET_SET_VERSION_CONFLICT", "素材批次归属或版本不一致，请重新读取批次。", 409);
        if (attachments.some((item) => (item.kind === "video" || videoAttachmentIDs.includes(item.asset_id)) && !activeVideoBatch.members.some((member) => member.asset_id === item.asset_id))) throw new ApiError("ASSET_SET_ATTACHMENTS_MISSING", "还有视频未加入当前批次，请先整理批次。", 409);
        submittedAttachments = videoBatchAttachmentRefs(activeVideoBatch, attachments);
        const otherAttachments = nonBatchAttachmentRefs(activeVideoBatch, attachments);
        if (!selected && otherAttachments.length) {
          submittedAttachments = [...submittedAttachments, ...otherAttachments];
          submittedVideoBatch = null;
        }
      }
    } catch (error) { setFailure(errorText(error)); return; }
    const payload = submissionPayload(content, capability, submittedAttachments, submittedVideoBatch);
    if (selected && pendingSubmission.current?.payload !== payload && !capabilities.some((entry) => composerSkillIdentity(entry) === composerSkillIdentity(selected) && capabilityDependenciesReady(entry) && trustedComposerViewKey(entry.view_key) !== "inspector")) {
      setFailure("所选 Skill 已更新或不可用，请重新选择后发送。"); return;
    }
    if (pendingSubmission.current?.payload !== payload) {
      if (pendingSubmission.current && !window.confirm("上一次发送结果未确认。修改内容或引用后再次发送可能创建新一轮，确认发送？")) return;
      pendingSubmission.current = { payload, key: createUUID() };
    }
    const key = pendingSubmission.current.key;
    if (submissionOwner) {
      try {
        const submittedContext = submittedVideoBatch ? { ...context, view: { ...context.view, asset_set_version_id: submittedVideoBatch.version.asset_set_version_id } } : context;
        const record = messageSubmission.prepare(submissionOwner, { content, capability, attachments: submittedAttachments, video_batch: submittedVideoBatch, context: submittedContext, clientInstanceID: messageClientInstanceID() }, "composer", key);
        savedMessage.current = record; setSavedPending(true);
        await sendSaved(record);
      } catch (error) { setFailure(errorText(error)); }
      return;
    }
    const controller = new AbortController();
    responseController.current = controller;
    setBusy(true); setFailure(""); setMenu(false);
    try {
      await onSend(content, capability, submittedAttachments, submittedVideoBatch, controller.signal, key);
      if (responseController.current === controller) clearSubmittedDraft();
    } catch (error) {
      if (responseController.current === controller) {
        setFailure(errorText(error));
        if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408) pendingSubmission.current = null;
      }
    }
    finally {
      if (responseController.current === controller) {
        responseController.current = null;
        setBusy(false);
      }
    }
  };
  useEffect(() => {
    const record = savedMessage.current;
    if (!record || starterDispatched.current || readOnly) return;
    starterDispatched.current = true;
    if (record.origin === "starter" && record.phase === "prepared") void sendSaved(record);
    else setRecoveryNotice("上次发送结果未确认，已恢复原消息。");
  }, [capabilities, onSend, projectID, readOnly]);
  const stopResponse = () => {
    const controller = responseController.current;
    responseController.current = null;
    controller?.abort();
    setBusy(false);
    setFailure("");
  };
  const acceptVideoBatch = async (next: AssetSetSnapshot) => {
    if (draftLocked()) return;
    const reconciled = await reconcileVideoBatchAttachments(projectID, videoBatch, next, attachments);
    setAttachments(reconciled);
    setVideoAttachmentIDs(reconciled.filter((item) => item.kind === "video" || next.members.some((member) => member.asset_id === item.asset_id)).map((item) => item.asset_id));
    setVideoBatch(next);
  };
  const prepareVideoBatch = async (videoAttachments: AttachmentRef[], capability = videoCapability) => {
    if (!videoAttachments.length || draftLocked()) return;
    retryVideoPreparation.current = { attachments: videoAttachments, capability };
    materialBusy.current = true;
    setUploading(true); setFailure("");
    try {
      const all = [...attachments.filter((item) => item.kind === "video" || videoAttachmentIDs.includes(item.asset_id)), ...videoAttachments];
      const unique = all.filter((item, index, list) => list.findIndex((entry) => entry.asset_id === item.asset_id) === index).map((item) => ({ ...item, kind: "video" }));
      const base = videoBatch?.asset_set.status === "collecting" ? videoBatch : null;
      const missing = unique.filter((item) => !base?.members.some((member) => member.asset_id === item.asset_id));
      const signature = canonicalJSONStringify([projectID, base?.version.asset_set_version_id, missing.map((item) => item.asset_id), capability?.input_binding?.asset_set_purpose]);
      if (batchAppendCommand.current?.signature !== signature) batchAppendCommand.current = { signature, key: createUUID() };
      const next = base && !missing.length ? base : await api.appendVideoBatch(projectID, base, missing, capability?.input_binding?.asset_set_purpose, batchAppendCommand.current.key);
      if (!videoBatchMatches(next, projectID, base?.asset_set.asset_set_id ?? next.asset_set.asset_set_id)) throw new ApiError("ASSET_SET_RECEIPT_INVALID", "视频批次回执与当前作品不一致。", 502);
      setAttachments((items) => [...items, ...unique.filter((item) => !items.some((entry) => entry.asset_id === item.asset_id))]);
      setVideoBatch(next);
      setBatchPreparationFailed(false);
      retryVideoPreparation.current = null;
      setBatchOpen(true);
    } catch (error) { setFailure(errorText(error)); setBatchPreparationFailed(true); setBatchOpen(false); }
    finally { materialBusy.current = false; setUploading(false); }
  };
  const chooseGeneral = () => {
    if (draftLocked() || busy || uploading || batchPending || batchPreparationFailed || materialBusy.current) return;
    setSelected(null); setMenu(false); setFailure("");
    const promptToClear = autoFilledPrompt.current;
    setValue((current) => current === promptToClear ? "" : current);
    autoFilledPrompt.current = null;
  };
  const chooseCapability = async (capability: ComposerRegistryEntry) => {
    if (draftLocked() || busy || uploading || batchPending || batchPreparationFailed || materialBusy.current) return;
    if (trustedComposerViewKey(capability.view_key) === "inspector" || !capabilityDependenciesReady(capability)) {
      setFailure(capability.user_message || "该 Skill 当前只能检查，不能调用。");
      setMenu(false);
      return;
    }
    if (selected && composerSkillIdentity(selected) === composerSkillIdentity(capability)) { chooseGeneral(); return; }
    setSelected(capability); setMenu(false); setFailure("");
    setValue((current) => {
      const next = nextSkillPrompt(current, autoFilledPrompt.current, capability);
      autoFilledPrompt.current = next.autoPrompt;
      return next.value;
    });
    if (trustedComposerViewKey(capability.view_key) !== "video_asset_set") return;
    if (videoBatch) { setBatchOpen(true); return; }
    const attachedVideos = attachments.filter((attachment) =>
      videoAttachmentIDs.includes(attachment.asset_id) || reusableVideos.some((asset) => asset.asset_id === attachment.asset_id),
    );
    if (attachedVideos.length) { await prepareVideoBatch(attachedVideos, capability); return; }
    if (reusableVideos.length === 1) {
      const asset = reusableVideos[0];
      await prepareVideoBatch([{ asset_id: asset.asset_id, asset_snapshot_id: asset.current_snapshot_id, display_name: asset.display_name || asset.original_filename }], capability);
      return;
    }
    if (reusableVideos.length > 1) setVideoPickerOpen(true);
  };
  const removeAttachment = (target: AttachmentRef) => {
    if (draftLocked() || busy || uploading || batchPending || batchPreparationFailed || materialBusy.current) return;
    const next = removeAttachmentReferences(attachments, [target.asset_id]);
    setAttachments(next);
    setVideoAttachmentIDs((ids) => ids.filter((id) => next.some((item) => item.asset_id === id)));
    if (videoBatch?.members.some((member) => !next.some((item) => item.asset_id === member.asset_id))) setVideoBatch(null);
  };
  const attachProjectMaterials = async (assets: Asset[]) => {
    if (draftLocked() || busy || uploading || batchPending || batchPreparationFailed || materialBusy.current || activeVideoBatch?.asset_set.status === "sealed") { setFailure("请先完成当前材料批次操作。"); return; }
    for (const asset of assets) {
      const current = projectAssets.find((item) => item.asset_id === asset.asset_id && item.project_id === projectID);
      if (!current || current.current_snapshot_id !== asset.current_snapshot_id) { setFailure("材料版本已更新，请重新选择。"); return; }
      const reason = materialUnavailableReason(current, selected);
      if (reason) { setFailure(reason); return; }
    }
    const references = assets.map(materialAttachment);
    setFailure(""); setUploadNotice("");
    setAttachments((items) => [...items, ...references.filter((item) => !items.some((existing) => existing.asset_id === item.asset_id))]);
    const videos = references.filter((item) => item.kind === "video");
    if (videos.length) {
      setVideoAttachmentIDs((items) => [...new Set([...items, ...videos.map((item) => item.asset_id)])]);
      if (!selected || trustedComposerViewKey(selected.view_key) === "video_asset_set") await prepareVideoBatch([...attachments.filter((item) => item.kind === "video"), ...videos], selected ?? videoCapability);
    }
    onClearSelection();
    document.querySelector<HTMLTextAreaElement>('.composer textarea[aria-label="给 Agent 的消息"]')?.focus();
  };
  useEffect(() => {
    if (draftLocked() || !materialRequest || handledMaterial.current === materialRequest.id) return;
    handledMaterial.current = materialRequest.id;
    void attachProjectMaterials([materialRequest.asset]);
    onMaterialHandled?.();
  }, [materialRequest, savedPending, storageFailure, readOnly]);
  const upload = async (files: FileList | null) => {
    if (!files?.length || draftLocked() || busy || uploading || batchPending || batchPreparationFailed || materialBusy.current || activeVideoBatch?.asset_set.status === "sealed") return;
    const selectedFiles = Array.from(files);
    const invalidVideo = selectedFiles.find((file) => fileKind(file) === "video" && file.size > maxVideoUploadBytes);
    if (invalidVideo) { setFailure(`${invalidVideo.name} 超过 500 MB，请压缩后重试。`); return; }
    const invalidArchive = selectedFiles.find((file) => fileKind(file) === "archive" && file.size > maxArchiveUploadBytes);
    if (invalidArchive) { setFailure(`${invalidArchive.name} 超过 1 GB，请拆分后重试。`); return; }
    const unsupported = selected && selectedFiles.find((file) => fileKind(file) !== "archive" && !capabilityAccepts(selected, fileKind(file)));
    if (unsupported) { setFailure(`${selected.label} 不接受 ${unsupported.name} 这类文件。`); return; }
    materialBusy.current = true; setUploading(true); setFailure(""); setUploadNotice("");
    const uploaded: AttachmentRef[] = [];
    let uploadFailure = "";
    try {
      for (const file of selectedFiles) {
        try {
          const references = await api.uploadFiles(projectID, [file]);
          uploaded.push(...references);
          setAttachments((items) => [...items, ...references]);
        } catch (error) { uploadFailure = `${file.name} 上传失败：${errorText(error)}`; break; }
      }
      const ignored = uploaded.flatMap((item) => item.ignored_entries ?? []);
      if (ignored.length) setUploadNotice(`ZIP 已上传，${ignored.length} 个不支持的文件未导入。`);
      const videos = uploaded.filter((item) => item.kind === "video" || (!item.kind && fileKind({ name: item.display_name, type: "" }) === "video"));
      if (videos.length) setVideoAttachmentIDs((items) => [...new Set([...items, ...videos.map((item) => item.asset_id)])]);
      if (videos.length && (!selected || trustedComposerViewKey(selected.view_key) === "video_asset_set")) await prepareVideoBatch(videos, selected ?? videoCapability);
    } catch (error) { setFailure(errorText(error)); }
    finally {
      if (uploadFailure) setFailure((current) => [current, uploadFailure].filter(Boolean).join("；"));
      materialBusy.current = false; setUploading(false); if (fileInput.current) fileInput.current.value = "";
    }
  };
  const visibleAttachments = attachments.filter((item) => !item.hidden);
  const displayedContext = savedMessage.current?.draft.context ?? context;
  const hasFeedback = Boolean(failure || storageFailure || recoveryNotice || savedPending || batchPreparationFailed || uploadNotice);
  return <>
    <div className={`composer ${running ? "running" : ""} ${hasFeedback ? "has-feedback" : ""}`}>
      <div className="composer-context">
        {displayedContext.selection ? <span className="selection-reference"><Sparkles size={13} /><span><strong>{targetSummary(displayedContext.selection)}</strong><small>{displayedContext.selection.display.selected_text_summary}</small></span><button type="button" aria-label="移除内容引用" title="移除内容引用" disabled={draftLocked() || busy} onClick={onClearSelection}><X size={13} /></button></span>
          : activeVideoBatch ? <button className="batch-context" disabled={draftLocked() || busy || uploading || batchPreparationFailed} onClick={() => setBatchOpen(true)}><FileText size={13} />视频批次 · {activeVideoBatch.members.filter((member) => member.included).length} 集 · {activeVideoBatch.asset_set.status === "sealed" ? "已确认" : "待确认"}</button>
          : visibleAttachments.length ? <>{visibleAttachments.map((item) => <span className="attachment-chip" key={item.asset_snapshot_id} title={item.display_name}><span>{item.display_name}</span><button aria-label={`移除 ${item.display_name}`} disabled={draftLocked() || busy || uploading || batchPending || batchPreparationFailed} onClick={() => removeAttachment(item)}><X size={12} /></button></span>)}</>
          : selected ? <><Sparkles size={13} />{selected.label}</>
          : displayedContext.view.artifact_label ? <span className="view-reference"><FileText size={13} />当前：{displayedContext.view.artifact_label}{displayedContext.view.scope_key ? ` · 第 ${scopeNumber(displayedContext.view.scope_key)} 集` : ""}</span>
          : "Agent 输入"}
        {running && <span className="run-context"><span className="context-dot" />{runSnapshot ? runStatusLabel(runSnapshot.run.status) : "运行中"}</span>}
      </div>
      <div className="composer-input">
        <textarea value={value} disabled={draftLocked() || busy} onChange={(event) => { if (draftLocked()) return; const next = event.target.value; if (next !== autoFilledPrompt.current) autoFilledPrompt.current = null; setValue(next); }} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing && event.nativeEvent.keyCode !== 229) { event.preventDefault(); void submit(); } }} placeholder={displayedContext.selection ? "说明希望如何处理引用内容…" : running ? "询问进度，或提出对当前产物的修改要求…" : "描述你想完成的内容，或选择一个 Skill…"} aria-label="给 Agent 的消息" />
      </div>
      {hasFeedback && <div className="composer-feedback">
        {failure && <span className="composer-error" aria-live="polite">{failure}</span>}
        {storageFailure && <span className="composer-error" aria-live="polite">{storageFailure}</span>}
        {recoveryNotice && <span className="composer-notice" aria-live="polite">{recoveryNotice}</span>}
        {savedPending && <div className="message-recovery-actions"><span>{savedMessage.current?.draft.capability ? `${savedMessage.current.draft.capability.label} · ${savedMessage.current.draft.capability.version}` : "通用 Agent"}</span>{cleanupPending ? <button className="secondary-button" disabled={busy} onClick={clearSaved}><RefreshCw size={14} />清理消息记录</button> : <button className="secondary-button" disabled={busy || readOnly} onClick={editAsNew}><Pencil size={14} />按新请求编辑</button>}</div>}
        {(storageFailure || savedPending && !busy && !cleanupPending) && <button className="secondary-button" disabled={busy} onClick={refreshSaved}><RefreshCw size={14} />读取待发送记录</button>}
        {batchPreparationFailed && <button type="button" className="secondary-button" disabled={uploading || batchPending} onClick={() => { const retry = retryVideoPreparation.current; if (retry) void prepareVideoBatch(retry.attachments, retry.capability); }}><RefreshCw size={15} />重试整理视频批次</button>}
        {uploadNotice && <span className="composer-notice" aria-live="polite">{uploadNotice}</span>}
      </div>}
      <div className="composer-actions"><div>
        <input ref={fileInput} className="visually-hidden" type="file" multiple disabled={draftLocked() || busy || uploading || batchPending || batchPreparationFailed || activeVideoBatch?.asset_set.status === "sealed"} accept={acceptedUploadTypes(selected)} onChange={(event) => void upload(event.target.files)} />
        <button className="composer-action" aria-label="添加文件" title="添加文件" disabled={draftLocked() || busy || uploading || batchPending || batchPreparationFailed || activeVideoBatch?.asset_set.status === "sealed"} onClick={() => fileInput.current?.click()}>{uploading ? <span className="mini-spinner" /> : <Paperclip size={17} />}</button>
        <button type="button" className="composer-action" aria-label="项目材料" title="项目材料" disabled={draftLocked() || busy || uploading || batchPending || batchPreparationFailed || activeVideoBatch?.asset_set.status === "sealed"} onClick={() => setMaterialPickerOpen(true)}><FolderOpen size={17} /></button>
        <div className="capability-control"><button className={`composer-action capability ${selected ? "selected" : ""}`} disabled={draftLocked() || busy || uploading || batchPending || batchPreparationFailed} onClick={() => setMenu(!menu)}><Sparkles size={15} /><span>Skill</span></button>{menu && <div className="capability-menu"><button type="button" onClick={chooseGeneral}><Bot size={15} /><span><strong>通用 Agent</strong></span>{!selected && <Check size={14} />}</button>{capabilities.map((capability) => { const supported = trustedComposerViewKey(capability.view_key) !== "inspector" && capabilityDependenciesReady(capability); return <button key={capability.capability_id} className={!supported ? "unsupported" : ""} aria-disabled={!supported} onClick={() => void chooseCapability(capability)}><span><strong>{capability.label}</strong><small>{supported ? capability.description : capability.user_message || "只读检查 · 依赖或视图不可用"}</small></span>{selected && composerSkillIdentity(selected) === composerSkillIdentity(capability) && <Check size={14} />}</button>; })}</div>}</div>
      </div><button className={`send-action ${busy ? "responding" : ""}`} aria-label={busy ? "停止等待" : queueMode ? "加入下一轮队列" : "发送"} title={busy ? "停止等待" : queueMode ? "加入下一轮队列" : "发送"} disabled={!busy && (readOnly || Boolean(storageFailure) || cleanupPending || !value.trim() || uploading || batchPending || batchPreparationFailed || Boolean(activeVideoBatch && activeVideoBatch.asset_set.status !== "sealed"))} onClick={() => busy ? stopResponse() : void submit()}>{busy ? <Square size={13} fill="currentColor" /> : <ArrowUp size={17} />}</button></div>
    </div>
    {materialPickerOpen && <ProjectMaterialPicker key={projectID} projectID={projectID} assets={projectAssets} capability={selected} onClose={() => setMaterialPickerOpen(false)} onConfirm={(assets) => { setMaterialPickerOpen(false); void attachProjectMaterials(assets); }} />}
    {videoBatch && <VideoBatchDialog open={batchOpen} projectID={projectID} snapshot={videoBatch} onChange={acceptVideoBatch} onPendingChange={setBatchPending} onClose={() => setBatchOpen(false)} />}
    {videoPickerOpen && <ExistingVideoPickerDialog assets={reusableVideos} onClose={() => setVideoPickerOpen(false)} onConfirm={async (assets) => { setVideoPickerOpen(false); await prepareVideoBatch(assets.map((asset) => ({ asset_id: asset.asset_id, asset_snapshot_id: asset.current_snapshot_id, display_name: asset.display_name || asset.original_filename }))); }} />}
  </>;
}

function ExistingVideoPickerDialog({ assets, onClose, onConfirm }: { assets: Asset[]; onClose: () => void; onConfirm: (assets: Asset[]) => Promise<void> }) {
  const [selectedIDs, setSelectedIDs] = useState(() => assets.map((asset) => asset.asset_id));
  const [busy, setBusy] = useState(false);
  return <div className="modal-layer"><section className="dialog video-picker-dialog" role="dialog" aria-modal="true" aria-labelledby="video-picker-title"><div className="dialog-head"><div><h2 id="video-picker-title">选择本次视频批次</h2><p>只会使用本次勾选的视频，不会自动合并其他材料。</p></div><button className="icon-button ghost" aria-label="关闭" onClick={onClose}><X size={17} /></button></div><div className="video-picker-list">{assets.map((asset) => { const checked = selectedIDs.includes(asset.asset_id); return <label key={asset.asset_id} className={checked ? "selected" : ""}><input type="checkbox" checked={checked} onChange={() => setSelectedIDs((items) => checked ? items.filter((id) => id !== asset.asset_id) : [...items, asset.asset_id])} /><FileText size={15} /><span><strong>{asset.display_name || asset.original_filename}</strong><small>已上传视频</small></span></label>; })}</div><div className="dialog-actions"><button className="secondary-button" disabled={busy} onClick={onClose}>取消</button><button className="primary-button" disabled={busy || selectedIDs.length === 0} onClick={() => { setBusy(true); void onConfirm(assets.filter((asset) => selectedIDs.includes(asset.asset_id))).finally(() => setBusy(false)); }}>{busy ? "正在准备…" : `使用 ${selectedIDs.length} 个视频`}</button></div></section></div>;
}

function CancelRunDialog({ action, onCancel, onConfirm }: { action: AvailableAction; onCancel: () => void; onConfirm: () => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const submitting = useRef(false);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const confirm = async () => {
    if (submitting.current) return;
    submitting.current = true; setBusy(true); setFailure("");
    try { await onConfirm(); }
    catch (error) { if (mounted.current) setFailure(errorText(error)); }
    finally { submitting.current = false; if (mounted.current) setBusy(false); }
  };
  return <div className="modal-layer"><section className="dialog" role="alertdialog" aria-modal="true" aria-labelledby="cancel-run-title"><div className="dialog-head"><h2 id="cancel-run-title">结束本次运行</h2><button className="icon-button ghost" aria-label="关闭" disabled={busy} onClick={onCancel}><X size={17} /></button></div><div className="dialog-body"><p>本次 Skill 运行将结束，未完成步骤不会继续。已完成并保存的产物仍会保留，但结束后不能从当前执行位置恢复。</p>{action.disabled_reason && <p>{action.disabled_reason}</p>}{failure && <p className="form-error" aria-live="polite">{failure}</p>}</div><div className="dialog-actions"><button className="secondary-button" disabled={busy} onClick={onCancel}>保留运行</button><button className="destructive-button" disabled={busy} onClick={() => void confirm()}>{busy ? "正在结束…" : "确认结束运行"}</button></div></section></div>;
}

function PartialContinueDialog({ action, tasks, onCancel, onConfirm }: { action: AvailableAction; tasks: TaskItem[]; onCancel: () => void; onConfirm: () => Promise<void> }) {
	const [busy, setBusy] = useState(false);
	const [failure, setFailure] = useState("");
	const submitting = useRef(false);
	const mounted = useRef(true);
	useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
	const succeeded = tasks.filter((task) => task.status === "succeeded");
	const failed = tasks.filter((task) => task.status === "failed");
	const confirm = async () => {
		if (submitting.current) return;
		submitting.current = true;
		setBusy(true);
		setFailure("");
		try { await onConfirm(); }
		catch (error) { if (mounted.current) setFailure(errorText(error)); }
		finally { submitting.current = false; if (mounted.current) setBusy(false); }
	};
	return <div className="modal-layer"><section className="dialog" role="alertdialog" aria-modal="true" aria-labelledby="partial-continue-title"><div className="dialog-head"><div><h2 id="partial-continue-title">按现有结果继续</h2><p>该批次有部分内容未生成。</p></div><button className="icon-button ghost" aria-label="关闭" disabled={busy} onClick={onCancel}><X size={17} /></button></div><div className="dialog-body"><p>将保留 {succeeded.length} 项成功结果，并跳过 {failed.length} 项失败内容。后续产物会标记材料不完整。</p>{failed.length > 0 && <p><strong>未生成：</strong>{failed.map((task) => taskProgressLabel(task)).join("、")}</p>}{action.disabled_reason && <p>{action.disabled_reason}</p>}{failure && <p className="form-error" aria-live="polite">{failure}</p>}</div><div className="dialog-actions"><button className="secondary-button" disabled={busy} onClick={onCancel}>返回重试</button><button className="primary-button" disabled={busy} onClick={() => void confirm()}>{busy ? "正在处理…" : "确认按现有结果继续"}</button></div></section></div>;
}

export function VideoBatchDialog({ snapshot, projectID, open = true, readOnly = false, onChange, onPendingChange, onClose }: { snapshot: AssetSetSnapshot; projectID: string; open?: boolean; readOnly?: boolean; onChange: (snapshot: AssetSetSnapshot) => void | Promise<void>; onPendingChange?: (pending: boolean) => void; onClose: () => void }) {
  const principal = useContext(PrincipalContext);
  const readonly = readOnly || principal?.role === "viewer";
  const [busy, setBusy] = useState(false);
  const [pending, setPending] = useState(false);
  const [allowIncomplete, setAllowIncomplete] = useState(false);
  const [failure, setFailure] = useState("");
  const [dirty, setDirty] = useState(false);
  const [draftVersion, setDraftVersion] = useState(snapshot.version.asset_set_version_id);
  const [episodeDrafts, setEpisodeDrafts] = useState(() => videoEpisodeDrafts(snapshot));
  type BatchCommand = { base: AssetSetSnapshot; key: string; seal: boolean; invoke: (key: string) => Promise<AssetSetSnapshot>; receipt?: AssetSetSnapshot };
  const command = useRef<BatchCommand | null>(null);
  const submitting = useRef(false), mounted = useRef(true);
  const owner = canonicalJSONStringify([projectID, snapshot.asset_set.asset_set_id]);
  const current = useRef({ owner, readonly, snapshot }); current.current = { owner, readonly, snapshot };
  const resetDrafts = (next: AssetSetSnapshot) => { setEpisodeDrafts(videoEpisodeDrafts(next)); setDraftVersion(next.version.asset_set_version_id); setDirty(false); setAllowIncomplete(false); };
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => { onPendingChange?.(pending); return () => onPendingChange?.(false); }, [pending, onPendingChange]);
  useEffect(() => { command.current = null; setPending(false); setFailure(""); resetDrafts(snapshot); }, [owner]);
  useEffect(() => { if (!dirty && !command.current) resetDrafts(snapshot); }, [snapshot.version.asset_set_version_id]);
  const stale = draftVersion !== snapshot.version.asset_set_version_id;
  const valid = videoBatchMatches(snapshot, projectID, snapshot.asset_set.asset_set_id);
  const interactionLocked = readonly || busy || pending || stale || !valid;
  const locked = interactionLocked || snapshot.asset_set.status !== "collecting";
  const completeness = snapshot.version.completeness;
  const missing = completeness.missing_episode_numbers;
  const hasUnconfirmedEpisodes = completeness.unrecognized_count > 0;
  const included = snapshot.members.filter((member) => member.included);
  const inferredAssignments = included.flatMap((member) => {
    if (member.episode_no) return [];
    const episodeNo = episodeNoFromFilename(member.filename_candidate.raw);
    return episodeNo && episodeNo <= 10000 ? [{ assetID: member.asset_id, episodeNo }] : [];
  });
  const canApplyInferredEpisodes = inferredAssignments.length > 0 && new Set(inferredAssignments.map((item) => item.episodeNo)).size === inferredAssignments.length;
  const active = () => mounted.current && current.current.owner === owner && !current.current.readonly;
  const execute = async (operation: BatchCommand) => {
    if (submitting.current || readonly) return;
    submitting.current = true; setBusy(true); setPending(true); setFailure("");
    try {
      if (!operation.receipt) {
        const receipt = await operation.invoke(operation.key);
        if (!active()) return;
        if (!videoBatchMatches(receipt, projectID, operation.base.asset_set.asset_set_id) || receipt.version.version !== operation.base.version.version + 1 || receipt.version.asset_set_version_id === operation.base.version.asset_set_version_id || receipt.version.status !== (operation.seal ? "sealed" : "draft")) throw new ApiError("ASSET_SET_RECEIPT_INVALID", "素材批次回执与当前操作不一致。", 502);
        operation.receipt = receipt;
      }
      const latest = await api.getAssetSet(operation.base.asset_set.asset_set_id);
      if (!active()) return;
      if (!videoBatchMatches(latest, projectID, operation.base.asset_set.asset_set_id) || latest.version.version < Math.max(operation.receipt.version.version, current.current.snapshot.version.version)) throw new ApiError("ASSET_SET_RECEIPT_INVALID", "素材批次读取结果已过期或归属不一致。", 502);
      const defaults = videoEpisodeDrafts(latest), oldDefaults = videoEpisodeDrafts(operation.base);
      const drafts = { ...defaults };
      for (const member of latest.members) {
        if (member.episode_no === null && episodeDrafts[member.asset_id] !== undefined && episodeDrafts[member.asset_id] !== oldDefaults[member.asset_id]) drafts[member.asset_id] = episodeDrafts[member.asset_id];
      }
      await onChange(latest);
      if (!active()) return;
      command.current = null; setPending(false); setEpisodeDrafts(drafts); setDraftVersion(latest.version.asset_set_version_id); setDirty(Object.keys(drafts).some((id) => drafts[id] !== defaults[id])); setAllowIncomplete(false);
      if (operation.seal && latest.asset_set.status === "sealed") onClose();
    } catch (error) {
      if (active()) {
        setFailure(errorText(error));
        if (!operation.receipt && error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") {
          command.current = null; setPending(false);
          try {
            const latest = await api.getAssetSet(operation.base.asset_set.asset_set_id);
            if (active() && videoBatchMatches(latest, projectID, operation.base.asset_set.asset_set_id) && latest.version.version >= current.current.snapshot.version.version) await onChange(latest);
          } catch { /* Keep the command rejection visible if refreshing also fails. */ }
        }
      }
    } finally { submitting.current = false; if (mounted.current) setBusy(false); }
  };
  const submit = (invoke: BatchCommand["invoke"], seal = false, reopen = false) => {
    if ((reopen ? interactionLocked || snapshot.asset_set.status !== "sealed" : locked) || submitting.current || command.current) return;
    const operation = { base: snapshot, key: createUUID(), seal, invoke };
    command.current = operation; void execute(operation);
  };
  const reload = async () => {
    if (submitting.current || command.current || dirty && !window.confirm("重新读取将放弃尚未提交的集号修改，继续吗？")) return;
    submitting.current = true; setBusy(true); setFailure("");
    try {
      const latest = await api.getAssetSet(snapshot.asset_set.asset_set_id);
      if (!mounted.current || current.current.owner !== owner) return;
      if (!videoBatchMatches(latest, projectID, snapshot.asset_set.asset_set_id) || latest.version.version < current.current.snapshot.version.version) throw new ApiError("ASSET_SET_RECEIPT_INVALID", "素材批次读取结果已过期或归属不一致。", 502);
      await onChange(latest); resetDrafts(latest);
    } catch (error) { if (mounted.current && current.current.owner === owner) setFailure(errorText(error)); }
    finally { submitting.current = false; if (mounted.current) setBusy(false); }
  };
  const close = () => { if (!submitting.current) onClose(); };
  const move = (index: number, direction: -1 | 1) => { const target = index + direction; if (target < 0 || target >= snapshot.members.length) return; submit((key) => api.swapVideoEpisodes(snapshot, snapshot.members[index].asset_id, target + 1, snapshot.members[target].asset_id, index + 1, key)); };
  const confirmEpisodeNo = (assetID: string) => {
    const episodeNo = Number(episodeDrafts[assetID]);
    if (!Number.isSafeInteger(episodeNo) || episodeNo < 1 || episodeNo > 10000) { setFailure("集号必须为 1 到 10000 的整数。"); return; }
    submit((key) => api.setVideoEpisodeNo(snapshot, assetID, episodeNo, key));
  };
  const cannotSeal = locked || !included.length || hasUnconfirmedEpisodes || completeness.duplicate_episode_numbers.length > 0 || completeness.failed_asset_ids.length > 0 || !completeness.order_confirmed || missing.length > 0 && !allowIncomplete;
  if (!open) return null;
  return <div className="modal-layer"><section className="dialog batch-dialog" role="dialog" aria-modal="true" aria-labelledby="batch-title">
    <div className="dialog-head"><div><h2 id="batch-title">确认视频批次</h2></div><button className="icon-button ghost" disabled={busy} aria-label="关闭" onClick={close}><X size={17} /></button></div>
    <div className="batch-summary"><span>已纳入 {included.length} / {snapshot.version.member_count} 集</span><span>{snapshot.version.completeness.recognized_episode_count} 集已识别集号</span>{hasUnconfirmedEpisodes && <strong>{snapshot.version.completeness.unrecognized_count} 集待处理</strong>}{!hasUnconfirmedEpisodes && missing.length > 0 && <strong>缺少第 {missing.join("、")} 集</strong>}</div>
    <div className="batch-list">{snapshot.members.map((member, index) => <div key={member.asset_id}>
      <input type="checkbox" aria-label={`纳入 ${member.filename_candidate.raw || `视频 ${index + 1}`}`} checked={member.included} disabled={locked} onChange={(event) => { const included = event.target.checked; submit((key) => api.setVideoBatchAssetIncluded(snapshot, member.asset_id, included, key)); }} />
      <div><strong>{member.filename_candidate.raw || member.episode_label || `视频 ${index + 1}`}</strong><small>{member.episode_no ? `第 ${member.episode_no} 集` : snapshot.members.length === 1 ? "未识别集号，建议设为第 1 集" : "未识别集号，请确认"}</small></div>
      <div className="batch-item-actions">
        {member.episode_no ? <span className="episode-recognized">第 {member.episode_no} 集</span> : <><label className="episode-number-field"><span>集号</span><input type="number" min="1" max="10000" step="1" disabled={locked || !member.included} value={episodeDrafts[member.asset_id] ?? ""} aria-label={`${member.filename_candidate.raw || `视频 ${index + 1}`}的集号`} onChange={(event) => { setDirty(true); setEpisodeDrafts((current) => ({ ...current, [member.asset_id]: event.target.value })); }} /></label>
        <button className="episode-confirm-button" aria-label={`确认${member.filename_candidate.raw || `视频 ${index + 1}`}的集号`} title="确认集号" disabled={locked || !member.included || Number(episodeDrafts[member.asset_id]) === member.episode_no} onClick={() => confirmEpisodeNo(member.asset_id)}>{snapshot.members.length === 1 ? "采用集号" : "确认"}</button></>}
        <button className="icon-button ghost" aria-label="上移" title="上移" disabled={locked || index === 0} onClick={() => move(index, -1)}><ArrowUp size={15} /></button>
        <button className="icon-button ghost" aria-label="下移" title="下移" disabled={locked || index === snapshot.members.length - 1} onClick={() => move(index, 1)}><ArrowDown size={15} /></button>
        <button className="icon-button ghost" aria-label={`移除 ${member.filename_candidate.raw || `视频 ${index + 1}`}`} title="从批次移除" disabled={locked} onClick={() => submit((key) => api.removeVideoBatchAsset(snapshot, member.asset_id, key))}><X size={15} /></button>
      </div>
    </div>)}</div>
    <div className="dialog-body batch-confirm">{hasUnconfirmedEpisodes && canApplyInferredEpisodes && <button className="secondary-button" disabled={locked} onClick={() => submit((key) => api.setVideoEpisodeNumbers(snapshot, inferredAssignments, key))}>一键识别并排序 {inferredAssignments.length} 集</button>}{!hasUnconfirmedEpisodes && missing.length > 0 && <label><input type="checkbox" disabled={locked} checked={allowIncomplete} onChange={(event) => setAllowIncomplete(event.target.checked)} />按不完整材料继续，分析中标记完整度</label>}
      {(failure || !valid || stale) && <p className="form-error" role="alert">{failure || (!valid ? "素材批次归属或版本不一致，无法提交。" : "批次已有新版本，未提交集号已保留。")}</p>}
      {pending && <button className="secondary-button" disabled={busy || readonly} onClick={() => { if (command.current) void execute(command.current); }}><RefreshCw size={15} />{command.current?.receipt ? "重新读取操作结果" : "重试原操作"}</button>}
      {!pending && <button className="icon-button ghost" aria-label="重新读取批次" title="重新读取批次" disabled={busy} onClick={() => void reload()}><RefreshCw size={15} /></button>}
    </div>
    <div className="dialog-actions"><button className="secondary-button" disabled={busy} onClick={close}>{snapshot.asset_set.status === "sealed" ? "返回" : "继续上传"}</button>{snapshot.asset_set.status === "sealed" ? <button className="secondary-button" disabled={interactionLocked} onClick={() => submit((key) => api.reopenVideoBatch(snapshot, key), false, true)}><RefreshCw size={15} />重新收集</button> : <button className="primary-button" disabled={cannotSeal} onClick={() => { if (!cannotSeal) submit((key) => api.sealVideoBatch(snapshot, allowIncomplete, key), true); }}>{busy ? "正在确认…" : "确认已上传完成"}</button>}</div>
  </section></div>;
}

function videoEpisodeDrafts(snapshot: AssetSetSnapshot) { return Object.fromEntries(snapshot.members.map((member) => [member.asset_id, String(member.episode_no ?? (snapshot.members.length === 1 ? 1 : member.episode_order))])); }
function videoBatchMatches(snapshot: AssetSetSnapshot, projectID: string, setID: string): boolean {
  const set = snapshot?.asset_set, version = snapshot?.version;
  return Boolean(set && version && set.project_id === projectID && set.asset_set_id === setID && version.asset_set_id === setID && set.current_version_id === version.asset_set_version_id && Number.isSafeInteger(version.version) && version.version > 0 && set.current_version === version.version && ["collecting", "sealed"].includes(set.status) && version.status === (set.status === "sealed" ? "sealed" : "draft") && Array.isArray(snapshot.members) && version.member_count === snapshot.members.length && new Set(snapshot.members.map((member) => member.asset_id)).size === snapshot.members.length);
}

function ConnectionFailure({ message, retry }: { message: string; retry: () => Promise<void> }) {
  return <div className="connection-failure" aria-live="polite"><CircleAlert size={20} /><div><strong>无法读取数据</strong><p>{message}</p></div><button className="secondary-button" onClick={() => void retry()}>重新加载</button></div>;
}
