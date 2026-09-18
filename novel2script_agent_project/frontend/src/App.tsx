import { useEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties, KeyboardEvent as ReactKeyboardEvent, PointerEvent as ReactPointerEvent } from "react";
import { Bot, FileText, FolderKanban, PanelLeft, PanelLeftOpen, PanelRightOpen } from "lucide-react";
import { createProject, deleteProject, deleteProjectFile, getRunSnapshot, health, listProjectFiles, listProjectMessages, listProjects, pauseRun, resolveApproval, resumeRun, sendProjectMessage, uploadProjectFile } from "./api/client";
import { mergeEvents, subscribeRunEvents } from "./api/events";
import { ApiError } from "./api/errors";
import { AgentPanel } from "./components/agent/AgentPanel";
import { ArtifactWorkspace } from "./components/artifacts/ArtifactWorkspace";
import { Button } from "./components/ui/Button";
import { ErrorBoundary } from "./components/ui/ErrorBoundary";
import { ProjectSidebar } from "./components/workbench/ProjectSidebar";
import { ProjectPicker } from "./components/workbench/ProjectPicker";
import {
  activeLabelFromRun,
	agentRequestFailureMessage,
  buildExecutionSteps,
  buildNavItems,
  mainSubtitle,
  mainTitle,
  materialFlow,
  novelFlow,
	runFailureMessage,
  tabLabel,
} from "./lib/workbenchLabels";
import {
  buildGenerationConfigPrompt,
  isNotFoundError,
  latestActiveArtifactsByType,
  latestUnconsumedSelection,
  localID,
  mergeArtifacts,
  normalizedGenerationConfig,
  readAttachment,
  restoredWorkspaceSummary,
  restoredGenerationConfigPrompt,
  sourceTextFromAttachments,
} from "./lib/workspaceHelpers";
import {
  adjustPaneWidth,
  DEFAULT_PANE_WIDTHS,
  parseStoredPaneWidth,
  type ResizablePane,
} from "./lib/workspaceLayout";
import type {
  ApprovalAction,
  ApprovalRequest,
  Artifact,
  ArtifactType,
  ArtifactUpdateResponse,
  ChatMessage,
  FileAttachment,
  GenerationConfig,
  Project,
  ProjectMessageResponse,
  Run,
  RunEvent,
  SelectionContext,
  SourceMode,
  ViewMode,
} from "./api/types";

type SourceFileAttachment = FileAttachment & { file_id?: string };
type WorkspacePane = "navigation" | "content" | "agent";
type ArtifactEditState = { artifactId: string; artifactType: ArtifactType; editing: boolean; dirty: boolean };
type PendingNavigation = { kind: "navigate"; view: ViewMode; artifactType: ArtifactType | null } | { kind: "reset" };

function readStoredWorkspace(projectID: string): { view: ViewMode; selectedArtifactType: ArtifactType | null } | null {
  try {
    const value = JSON.parse(window.localStorage.getItem(`n2s.workspace.${projectID}.view`) || "null") as Record<string, unknown> | null;
    if (!value || !["artifacts", "script", "events"].includes(String(value.view))) return null;
    return {
      view: value.view as ViewMode,
      selectedArtifactType: typeof value.selectedArtifactType === "string" ? value.selectedArtifactType as ArtifactType : null,
    };
  } catch {
    return null;
  }
}

function targetEpisodeCount(run: Run | null, latestByType: Map<ArtifactType, Artifact>) {
  const metadataConfig = readRecord(readRecord(run?.metadata).generation_config);
  const configured = Number(metadataConfig.target_episode_count || 0);
  if (configured > 0) return configured;
  const episodeCards = latestByType.get("episode_cards");
  const episodes = Array.isArray(episodeCards?.payload.episodes) ? episodeCards.payload.episodes : [];
  return episodes.length;
}

function readRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

export default function App() {
	return <WorkbenchApp />;
}

function WorkbenchApp() {
  const [backendStatus, setBackendStatus] = useState("Backend: checking");
  const [backendError, setBackendError] = useState(false);
  const [artifactSyncNotice, setArtifactSyncNotice] = useState("");
  const [sourceMode, setSourceMode] = useState<SourceMode>("auto");
  const [inputText, setInputText] = useState("");
  const [sourcePreviewText, setSourcePreviewText] = useState("");
  const [composerText, setComposerText] = useState("");
  const [attachments, setAttachments] = useState<FileAttachment[]>([]);
  const [attachmentFileIDs, setAttachmentFileIDs] = useState<string[]>([]);
  const [sourceFiles, setSourceFiles] = useState<SourceFileAttachment[]>([]);
  const [project, setProject] = useState<Project | null>(null);
  const [projects, setProjects] = useState<Project[]>([]);
  const [projectPickerOpen, setProjectPickerOpen] = useState(true);
  const [projectPickerBusy, setProjectPickerBusy] = useState(false);
  const [projectPickerError, setProjectPickerError] = useState("");
  const [run, setRun] = useState<Run | null>(null);
  const [approvalRequest, setApprovalRequest] = useState<ApprovalRequest | null>(null);
  const [events, setEvents] = useState<RunEvent[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [messages, setMessages] = useState<ChatMessage[]>([
    {
      id: localID("msg"),
      role: "agent",
      text: "你好，我是主 Agent。你可以先问流程，也可以发小说原文、梗概或短剧灵感；只有你明确要求生成或改编时，我才会启动剧本流程。",
    },
  ]);
  const [isThinking, setIsThinking] = useState(false);
  const [view, setView] = useState<ViewMode>("artifacts");
  const [selectedArtifactType, setSelectedArtifactType] = useState<ArtifactType | null>("source_input");
  const [selectionContext, setSelectionContext] = useState<SelectionContext | null>(null);
  const [activeWorkspacePane, setActiveWorkspacePane] = useState<WorkspacePane>("content");
  const [sidebarOpen, setSidebarOpen] = useState(() => window.localStorage.getItem("n2s.sidebarOpen") !== "false");
  const [agentOpen, setAgentOpen] = useState(() => window.localStorage.getItem("n2s.agentOpen") !== "false");
  const [sidebarWidth, setSidebarWidth] = useState(() => parseStoredPaneWidth("sidebar", window.localStorage.getItem("n2s.sidebarWidth")));
  const [agentWidth, setAgentWidth] = useState(() => parseStoredPaneWidth("agent", window.localStorage.getItem("n2s.agentWidth")));
  const [resizingPane, setResizingPane] = useState<ResizablePane | null>(null);
  const [artifactEditState, setArtifactEditState] = useState<ArtifactEditState | null>(null);
  const [pendingNavigation, setPendingNavigation] = useState<PendingNavigation | null>(null);
  const [editSaveRequest, setEditSaveRequest] = useState(0);
  const [editDiscardRequest, setEditDiscardRequest] = useState(0);
  const [navigationSavePending, setNavigationSavePending] = useState(false);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const timelineRef = useRef<HTMLDivElement | null>(null);
  const artifactBodyRef = useRef<HTMLElement | null>(null);
  const focusedApprovalIDRef = useRef<string | null>(null);
  const artifactEditStateRef = useRef<ArtifactEditState | null>(null);
  const projectIDRef = useRef("");
  const workspacePersistenceProjectIDRef = useRef("");

  useEffect(() => {
    artifactEditStateRef.current = artifactEditState;
  }, [artifactEditState]);

  useEffect(() => {
    projectIDRef.current = project?.project_id || "";
  }, [project?.project_id]);

  useEffect(() => {
    window.localStorage.setItem("n2s.sidebarOpen", String(sidebarOpen));
  }, [sidebarOpen]);

  useEffect(() => {
    window.localStorage.setItem("n2s.agentOpen", String(agentOpen));
  }, [agentOpen]);

  useEffect(() => {
    window.localStorage.setItem("n2s.sidebarWidth", String(sidebarWidth));
  }, [sidebarWidth]);

  useEffect(() => {
    window.localStorage.setItem("n2s.agentWidth", String(agentWidth));
  }, [agentWidth]);

  useEffect(() => {
    if (!project?.project_id) return;
    if (workspacePersistenceProjectIDRef.current !== project.project_id) return;
    window.localStorage.setItem(`n2s.workspace.${project.project_id}.view`, JSON.stringify({ view, selectedArtifactType }));
  }, [project?.project_id, selectedArtifactType, view]);

  useEffect(() => {
    function warnBeforeUnload(event: BeforeUnloadEvent) {
      if (!artifactEditStateRef.current?.dirty) return;
      event.preventDefault();
      event.returnValue = "";
    }
    window.addEventListener("beforeunload", warnBeforeUnload);
    return () => window.removeEventListener("beforeunload", warnBeforeUnload);
  }, []);

  function focusApproval(approval?: ApprovalRequest | null) {
    const approvalID = approval?.approval_request_id || null;
    if (!approvalID || focusedApprovalIDRef.current === approvalID) return;
    if (artifactEditStateRef.current?.dirty) return;
    focusedApprovalIDRef.current = approvalID;
  }

  function applyNavigation(next: Extract<PendingNavigation, { kind: "navigate" }>) {
    setView(next.view);
    setSelectedArtifactType(next.artifactType);
    setSelectionContext(null);
    setActiveWorkspacePane("content");
  }

  function requestNavigation(next: Omit<Extract<PendingNavigation, { kind: "navigate" }>, "kind">) {
    const action: PendingNavigation = { kind: "navigate", ...next };
    if (artifactEditStateRef.current?.dirty) {
      setPendingNavigation(action);
      return;
    }
    applyNavigation(action);
  }

  function requestWorkspaceReset() {
    if (artifactEditStateRef.current?.dirty) {
      setPendingNavigation({ kind: "reset" });
      return;
    }
    resetWorkspace();
  }

  function finishPendingNavigation(saved: boolean) {
    setNavigationSavePending(false);
    if (!saved || !pendingNavigation) return;
    const next = pendingNavigation;
    setPendingNavigation(null);
    setArtifactEditState(null);
    if (next.kind === "reset") resetWorkspace();
    else applyNavigation(next);
  }

  function discardAndContinue() {
    if (!pendingNavigation) return;
    const next = pendingNavigation;
    setPendingNavigation(null);
    setArtifactEditState(null);
    setEditDiscardRequest((current) => current + 1);
    if (next.kind === "reset") resetWorkspace();
    else applyNavigation(next);
  }

  useEffect(() => {
    let cancelled = false;

    async function loadProjectIndex() {
      try {
        const payload = await health();
        if (cancelled) return;
        setBackendStatus(`Backend: ${payload.runtime || "ok"} · Store: ${payload.store || "memory"}`);
        setBackendError(false);

        const projectPayload = await listProjects();
        if (cancelled) return;
        setProjects(projectPayload.projects);
        const activeProjectID = window.localStorage.getItem("n2s.activeProjectId") || "";
        const activeProject = projectPayload.projects.find((item) => item.project_id === activeProjectID);
        if (activeProject && !projectIDRef.current) {
          await openProjectWorkspace(activeProject);
        }
      } catch {
        if (cancelled) return;
        setBackendStatus("Backend: offline");
        setBackendError(true);
        setProjectPickerError("无法读取作品列表，请确认后端服务可用。");
      }
    }

    void loadProjectIndex();
    return () => {
      cancelled = true;
    };
  }, []);

  function rememberProject(nextProject: Project) {
    setProjects((current) => {
      const next = current.filter((item) => item.project_id !== nextProject.project_id);
      next.push(nextProject);
      return next;
    });
  }

  async function openProjectWorkspace(nextProject: Project) {
    if (artifactEditStateRef.current?.dirty) {
      setProjectPickerError("当前产物有未保存修改，请先保存或放弃修改后再切换作品。");
      return;
    }
    setProjectPickerBusy(true);
    setProjectPickerError("");
    workspacePersistenceProjectIDRef.current = "";
    projectIDRef.current = nextProject.project_id;
    setProject(nextProject);
    setRun(null);
    setEvents([]);
    setArtifacts([]);
    setApprovalRequest(null);
    setSelectionContext(null);
    setAttachments([]);
    setAttachmentFileIDs([]);
    setSourceFiles([]);
    setSourcePreviewText("");
    setInputText("");
    focusedApprovalIDRef.current = null;

    try {
      const [filePayload, messagePayload, snapshot] = await Promise.all([
        listProjectFiles(nextProject.project_id),
        listProjectMessages(nextProject.project_id),
        nextProject.active_run_id ? getRunSnapshot(nextProject.active_run_id) : Promise.resolve(null),
      ]);
      if (projectIDRef.current !== nextProject.project_id) return;

      const projectFiles = filePayload.files || [];
      const files = projectFiles.map((file) => ({
        file_id: file.file_id,
        file_name: file.filename,
        mime_type: file.mime_type,
        size: file.size_bytes,
        text_content: file.text_preview,
      }));
      const fileByID = new Map(projectFiles.map((file) => [file.file_id, file]));
      const storedWorkspace = readStoredWorkspace(nextProject.project_id);
      const restoredMessages: ChatMessage[] = (messagePayload.messages || []).map((message) => ({
        id: message.message_id,
        role: message.role === "system" ? "agent" : message.role,
        text: message.content,
        selectionContext: message.selection_context || null,
        attachments: (message.attachments || []).map((attachment) => {
          const file = typeof attachment === "string" ? fileByID.get(attachment) : attachment;
          return {
            file_name: typeof file === "object" && file && "filename" in file ? String(file.filename) : String(attachment),
            mime_type: typeof file === "object" && file && "mime_type" in file ? String(file.mime_type || "") : "",
            size: typeof file === "object" && file && "size_bytes" in file ? Number(file.size_bytes || 0) : 0,
          };
        }),
      }));
	  if (!snapshot) {
		const restoredConfig = restoredGenerationConfigPrompt(messagePayload.messages || [], nextProject.source_mode);
		if (restoredConfig) {
			const index = restoredMessages.findIndex((message) => message.id === restoredConfig.message_id);
			if (index >= 0) restoredMessages[index] = { ...restoredMessages[index], generationConfigPrompt: restoredConfig.prompt };
		}
	  }
	  const restoredSelection = latestUnconsumedSelection(messagePayload.messages || []);

      if (snapshot) {
        const lastAgentIndex = [...restoredMessages].reverse().findIndex((message) => message.role === "agent");
        if (lastAgentIndex >= 0) {
          const index = restoredMessages.length - 1 - lastAgentIndex;
          restoredMessages[index] = {
            ...restoredMessages[index],
			text: snapshot.run.status === "failed" ? runFailureMessage(snapshot.events || []) : restoredMessages[index].text,
            approval: snapshot.approval_request || undefined,
            steps: buildExecutionSteps(snapshot.events || []),
            activeStep: activeLabelFromRun(snapshot.run),
          };
        }
        setRun(snapshot.run);
        setEvents(snapshot.events || []);
        setArtifacts(snapshot.artifacts || []);
        setApprovalRequest(snapshot.approval_request || null);
        if (storedWorkspace) {
          setView(storedWorkspace.view);
          setSelectedArtifactType(storedWorkspace.selectedArtifactType ?? "source_input");
        } else if (snapshot.run.status === "completed") {
          setView("script");
          setSelectedArtifactType(null);
        } else {
          setView("artifacts");
          focusApproval(snapshot.approval_request);
        }
      } else {
        setView(storedWorkspace?.view || "artifacts");
        setSelectedArtifactType(storedWorkspace?.selectedArtifactType ?? "source_input");
      }

      setSourceMode(nextProject.source_mode);
      setSourceFiles(files);
      setSourcePreviewText(files.map((file) => file.text_content || "").filter(Boolean).join("\n\n"));
      setMessages(restoredMessages.length ? restoredMessages : [{
        id: localID("msg"),
        role: "agent",
        text: snapshot
          ? restoredWorkspaceSummary(nextProject.title, snapshot.run.status, snapshot.artifacts || [], Boolean(snapshot.approval_request))
          : "这是一个新的作品工作区。你可以先聊天或上传材料；只有明确要求生成时，我才会启动流程。",
        steps: snapshot ? buildExecutionSteps(snapshot.events || []) : undefined,
        activeStep: snapshot ? activeLabelFromRun(snapshot.run) : undefined,
        approval: snapshot?.approval_request || undefined,
      }]);
	  setSelectionContext(restoredSelection);
      window.localStorage.setItem("n2s.activeProjectId", nextProject.project_id);
      workspacePersistenceProjectIDRef.current = nextProject.project_id;
      setProjectPickerOpen(false);
      window.requestAnimationFrame(() => {
        const scrollTop = Number(window.localStorage.getItem(`n2s.workspace.${nextProject.project_id}.scrollTop`) || 0);
        artifactBodyRef.current?.scrollTo({ top: Number.isFinite(scrollTop) ? scrollTop : 0 });
      });
    } catch (error) {
      setProjectPickerError(error instanceof Error ? error.message : String(error));
    } finally {
      setProjectPickerBusy(false);
    }
  }

  async function createProjectWorkspace(title: string, mode: SourceMode) {
    setProjectPickerError("");
    const response = await createProject({ title, source_mode: mode });
    rememberProject(response.project);
    await openProjectWorkspace(response.project);
  }

  async function deleteProjectWorkspace(target: Project) {
    if (target.project_id === project?.project_id && artifactEditStateRef.current?.dirty) {
      setProjectPickerError("当前产物有未保存修改，请先保存或放弃修改后再删除作品。");
      return;
    }
    setProjectPickerBusy(true);
    setProjectPickerError("");
    try {
      await deleteProject(target.project_id);
      const remaining = projects.filter((item) => item.project_id !== target.project_id);
      setProjects(remaining);
      window.localStorage.removeItem(`n2s.workspace.${target.project_id}.view`);
      window.localStorage.removeItem(`n2s.workspace.${target.project_id}.scrollTop`);
      if (target.project_id !== project?.project_id) return;

      window.localStorage.removeItem("n2s.activeProjectId");
      artifactEditStateRef.current = null;
      setArtifactEditState(null);
      resetWorkspace();
      const nextProject = remaining.slice().sort((left, right) => String(right.updated_at || "").localeCompare(String(left.updated_at || "")))[0];
      if (nextProject) await openProjectWorkspace(nextProject);
    } catch (error) {
      setProjectPickerError(error instanceof Error ? error.message : String(error));
      throw error;
    } finally {
      setProjectPickerBusy(false);
    }
  }

  useEffect(() => {
    timelineRef.current?.scrollTo({ top: timelineRef.current.scrollHeight });
  }, [messages, isThinking]);

  useEffect(() => {
    if (!run?.run_id || (run.status !== "running" && run.status !== "waiting_approval")) return;
    let cancelled = false;
    const activeProjectID = project?.project_id || "";

    async function refreshRun() {
      if (!run?.run_id) return;
      try {
        const snapshot = await getRunSnapshot(run.run_id);
        if (cancelled || projectIDRef.current !== activeProjectID || snapshot.run.project_id !== activeProjectID) return;
        setRun(snapshot.run);
        if (snapshot.events) setEvents((current) => mergeEvents(current, snapshot.events || []));
        if (snapshot.artifacts) setArtifacts((current) => mergeArtifacts(current, snapshot.artifacts || []));
        setApprovalRequest(snapshot.approval_request || null);
        if (snapshot.run.status === "waiting_approval") {
          focusApproval(snapshot.approval_request);
        }
      } catch (error) {
        if (!cancelled && isNotFoundError(error)) {
          expireActiveRun("当前运行态已失效，可能是后端刚重启过。旧确认卡已经清理，请重新发送生成或修改请求。");
          return;
        }
        if (!cancelled) setBackendStatus(`Backend: ${error instanceof Error ? error.message : "poll failed"}`);
      }
    }

    void refreshRun();
    let refreshTimer = 0;
    const closeStream = subscribeRunEvents(run.run_id, (event) => {
      if (cancelled) return;
      setEvents((current) => mergeEvents(current, [event]));
      if (["artifact_created", "artifact_updated", "script_batch_inserted", "approval_requested", "approval_resolved", "progress_updated", "run_completed", "step_failed", "run_paused", "run_resumed"].includes(event.type)) {
        window.clearTimeout(refreshTimer);
        refreshTimer = window.setTimeout(() => void refreshRun(), 120);
      }
    }, () => {
      if (!cancelled) setBackendStatus("Backend: 实时连接正在重连，状态将由快照继续同步");
    });
    const timer = run.status === "running" ? window.setInterval(refreshRun, 15000) : 0;
    return () => {
      cancelled = true;
      closeStream();
      window.clearTimeout(refreshTimer);
      if (timer) window.clearInterval(timer);
    };
  }, [project?.project_id, run?.run_id, run?.status]);

  useEffect(() => {
    if (!run) return;
    const steps = buildExecutionSteps(events);
    const activeStep = activeLabelFromRun(run);
    setMessages((current) => {
      const next = [...current];
      const progressIndex = [...next].reverse().findIndex((message) => message.role === "agent" && Boolean(message.steps?.length));
      const fallbackIndex = [...next].reverse().findIndex((message) => message.role === "agent");
      const reverseIndex = progressIndex >= 0 ? progressIndex : fallbackIndex;
      if (reverseIndex < 0) return current;
      const index = next.length - 1 - reverseIndex;
      next[index] = {
        ...next[index],
		text: run.status === "failed" ? runFailureMessage(events) : next[index].text,
        steps,
        activeStep,
        approval: approvalRequest || undefined,
      };
      return next;
    });
  }, [events, run, approvalRequest]);

  const effectiveMode = useMemo(() => {
    if (run?.source_mode === "novel" || run?.source_mode === "non_novel") return run.source_mode;
    if (sourceMode === "novel" || sourceMode === "non_novel") return sourceMode;
    return "auto";
  }, [run?.source_mode, sourceMode]);

  const visibleTypes = useMemo<ArtifactType[]>(() => {
    if (effectiveMode === "novel") return novelFlow;
    if (effectiveMode === "non_novel") return materialFlow;
    return ["source_input"];
  }, [effectiveMode]);

  const latestByType = useMemo(() => {
    return latestActiveArtifactsByType(artifacts);
  }, [artifacts]);

  const currentArtifact = useMemo(() => {
    if (selectionContext?.artifact_id) {
      return artifacts.find((artifact) => artifact.artifact_id === selectionContext.artifact_id) || null;
    }
    if (selectedArtifactType) return latestByType.get(selectedArtifactType) || null;
    if (view === "script") return latestByType.get("scripts") || latestByType.get("script_unit") || null;
    return null;
  }, [artifacts, latestByType, selectedArtifactType, selectionContext?.artifact_id, view]);

  async function submitToAgent(messageOverride?: string, displayText?: string, configOverride?: GenerationConfig, sourceModeOverride?: SourceMode, fileIDsOverride?: string[], preserveComposerContext = false) {
    const message = (messageOverride ?? composerText).trim();
    const useComposerContext = !messageOverride || preserveComposerContext;
    const outgoingAttachments = useComposerContext ? attachments : [];
    const outgoingFileIDs = fileIDsOverride ?? (useComposerContext ? attachmentFileIDs : []);
    const outgoingSelectionContext = useComposerContext ? selectionContext : null;
    const mergedMessage = buildEffectiveMessage(message, outgoingAttachments);
    if (!mergedMessage && outgoingFileIDs.length === 0) {
      pushAgentMessage("你可以直接输入问题，或添加小说原文、梗概、素材文件。");
      return;
    }

    if (useComposerContext) {
      setMessages((current) => current.map((item) => (
        item.generationConfigPrompt ? { ...item, generationConfigPrompt: undefined } : item
      )));
    }
    pushUserMessage(displayText || message || "发送附件", outgoingAttachments, outgoingSelectionContext);
    if (useComposerContext) {
      setComposerText("");
      setAttachments([]);
      setAttachmentFileIDs([]);
      setSelectionContext(null);
    }
    setIsThinking(true);

    try {
      const activeProject = await ensureProject();
      const payload = await sendProjectMessage(activeProject.project_id, {
        content: mergedMessage,
        display_content: displayText || undefined,
        source_mode_hint: sourceModeOverride || sourceMode,
        file_ids: outgoingFileIDs,
        generation_config: normalizedGenerationConfig(configOverride || {}),
        client_context: {
          current_artifact_id: currentArtifact?.artifact_id,
          current_view: view,
        },
        selection_context: outgoingSelectionContext,
      });
      applyProjectMessageResponse(payload);
    } catch (error) {
	  if (error instanceof ApiError && error.code === "WORKSPACE_BUSY") {
		pushAgentMessage("作品状态正在同步，本次请求结果暂时无法确认。我会刷新当前运行状态，请先查看最新结果，不要立即重复提交。");
		if (run?.run_id) {
			void getRunSnapshot(run.run_id).then((snapshot) => {
				setRun(snapshot.run);
				setEvents(snapshot.events || []);
				setArtifacts(snapshot.artifacts || []);
				setApprovalRequest(snapshot.approval_request || null);
			}).catch(() => undefined);
		}
	  } else {
		pushAgentMessage(agentRequestFailureMessage(error instanceof Error ? error.message : String(error)));
		if (outgoingSelectionContext) setSelectionContext(outgoingSelectionContext);
	  }
    } finally {
      setIsThinking(false);
    }
  }

  async function submitGenerationConfig(promptMessageID: string, originalMessage: string, config: GenerationConfig, promptSourceMode: SourceMode, fileIDs: string[]) {
    const normalized = normalizedGenerationConfig(config);
    if (!normalized?.target_episode_count || !normalized.episode_duration_minutes) {
      pushAgentMessage("请先填写目标集数和单集时长。");
      return;
    }
    setMessages((current) =>
      current.map((message) => (message.id === promptMessageID ? { ...message, generationConfigPrompt: undefined } : message)),
    );
    await submitToAgent(originalMessage, `确认生成配置：${normalized.target_episode_count} 集，每集 ${normalized.episode_duration_minutes} 分钟`, normalized, promptSourceMode, fileIDs);
  }

  async function resolveCurrentApproval(action: ApprovalAction) {
    if (action === "approve" && composerText.trim()) {
      await submitToAgent();
      return;
    }
    if (!approvalRequest?.approval_request_id) {
      await submitToAgent(action === "approve" ? "确认继续" : "暂停", action === "approve" ? "确认继续" : "暂停");
      return;
    }
    const actionText: Partial<Record<ApprovalAction, string>> = {
      approve: "确认继续",
      pause: "暂停",
      keep_downstream: "保留现有后续内容",
      regenerate_downstream: "重新生成受影响内容",
    };
    pushUserMessage(actionText[action] || action, []);
    setIsThinking(true);
    try {
      const payload = await resolveApproval(approvalRequest.approval_request_id, {
        action,
        client_context: {
          current_artifact_id: currentArtifact?.artifact_id,
        },
      });
      if (payload.project) {
        setProject(payload.project);
        rememberProject(payload.project);
      }
      if (payload.run) setRun(payload.run);
      if (payload.events) setEvents((current) => mergeEvents(current, payload.events || []));
      if (payload.artifacts) setArtifacts((current) => mergeArtifacts(current, payload.artifacts || []));
      setApprovalRequest(payload.approval_request || null);
      setSelectionContext(null);
      setMessages((current) => [
        ...current,
        {
          id: localID("msg"),
          role: "agent",
          text: payload.agent_message?.content || (action === "regenerate_downstream"
            ? "已确认重新生成受影响的后续内容。旧内容会保留到对应新版本生成完成。"
            : action === "keep_downstream"
              ? "已保留现有后续内容，本次修改已完成。"
              : action === "approve"
                ? "已收到确认，我会继续推进后续生成。"
                : "已暂停当前流程，你可以继续补充修改要求。"),
          steps: buildExecutionSteps(payload.events || []),
          activeStep: activeLabelFromRun(payload.run),
        },
      ]);
    } catch (error) {
      if (isNotFoundError(error)) {
        expireActiveRun("当前确认请求已失效，可能是后端重启后旧审批 ID 已不存在。请重新发送生成或修改请求。");
        return;
      }
      pushAgentMessage(`审批请求失败：${error instanceof Error ? error.message : String(error)}`);
    } finally {
      setIsThinking(false);
    }
  }

  async function setRunningPaused(paused: boolean) {
    if (!run?.run_id) return;
    setIsThinking(true);
    try {
      const payload = paused ? await pauseRun(run.run_id) : await resumeRun(run.run_id);
      if (payload.project) { setProject(payload.project); rememberProject(payload.project); }
      setRun(payload.run);
      if (payload.events) setEvents((current) => mergeEvents(current, payload.events || []));
      if (payload.artifacts) setArtifacts((current) => mergeArtifacts(current, payload.artifacts || []));
      pushAgentMessage(paused ? "已暂停当前生成，已完成的内容已保留。点击继续后会从当前任务恢复。" : "已从暂停位置继续生成。");
    } catch (error) {
      pushAgentMessage(`${paused ? "暂停" : "继续"}失败：${error instanceof Error ? error.message : String(error)}`);
    } finally { setIsThinking(false); }
  }

  function expireActiveRun(message: string) {
    setRun(null);
    setApprovalRequest(null);
    setEvents([]);
    setArtifacts([]);
    setView("script");
    setSelectedArtifactType(null);
    setMessages((current) => [
      ...current.map((item) => ({ ...item, approval: undefined })),
      {
        id: localID("msg"),
        role: "agent",
        text: message,
      },
    ]);
  }
  function buildEffectiveMessage(message: string, outgoingAttachments: FileAttachment[]) {
    if (message) return message;
    if (inputText.trim() && outgoingAttachments.length === 0) return inputText.trim();
    return message;
  }

  async function ensureProject() {
    if (project) return project;
    setProjectPickerOpen(true);
    throw new Error("请先在作品中心选择或新建作品。");
  }

  function applyProjectMessageResponse(payload: ProjectMessageResponse) {
    if (payload.project) {
      setProject(payload.project);
      rememberProject(payload.project);
    }
    if (payload.run) setRun(payload.run);
    if (payload.events) setEvents((current) => mergeEvents(current, payload.events || []));
    const incomingArtifacts = payload.artifacts || [];
    if (incomingArtifacts.length) setArtifacts((current) => mergeArtifacts(current, incomingArtifacts));
    setApprovalRequest(payload.approval_request || null);

    const nextEvents = payload.events ? mergeEvents(events, payload.events) : events;
    const steps = buildExecutionSteps(nextEvents);
    const text = payload.agent_message?.content || "我已处理。";
    const generationConfigPrompt = buildGenerationConfigPrompt(payload, sourceMode);
    const action = payload.decision?.next_action || "";
    if (["revise_checkpoint", "start_run", "approve_run", "pause_run", "resume_run", "rerun_step"].includes(action)) setSelectionContext(null);
    const shouldAttachRunProgress = Boolean(payload.run) && (
      payload.run?.status === "running" ||
      payload.run?.status === "waiting_approval" ||
      payload.run?.status === "paused" ||
      payload.run?.status === "failed" ||
      (Boolean(action) && action !== "reply")
    );
    const shouldAttachApproval =
      Boolean(payload.approval_request?.approval_request_id) &&
      payload.approval_request?.approval_request_id !== approvalRequest?.approval_request_id;

    setMessages((current) => [
      ...current,
      {
        id: payload.agent_message?.message_id || localID("msg"),
        role: "agent",
        text,
        generationConfigPrompt,
        approval: shouldAttachApproval ? payload.approval_request || undefined : undefined,
        steps: shouldAttachRunProgress ? steps : undefined,
        activeStep: shouldAttachRunProgress ? activeLabelFromRun(payload.run || undefined) : undefined,
      },
    ]);

    if (payload.run?.status === "waiting_approval") {
      focusApproval(payload.approval_request || null);
    }
  }
  function pushUserMessage(text: string, files: FileAttachment[], selection?: SelectionContext | null) {
    setMessages((current) => [
      ...current,
      {
        id: localID("msg"),
        role: "user",
        text,
        selectionContext: selection || null,
        attachments: files.map((file) => ({
          file_name: file.file_name,
          mime_type: file.mime_type,
          size: file.size,
        })),
      },
    ]);
  }

  function pushAgentMessage(text: string) {
    setMessages((current) => [...current, { id: localID("msg"), role: "agent", text }]);
  }

  async function addFiles(fileList: FileList | null) {
    if (!fileList) return;
    const next: FileAttachment[] = [];
    const nextFileIDs: string[] = [];
    const nextSourceFiles: SourceFileAttachment[] = [];
    const activeProject = await ensureProject();
    for (const file of Array.from(fileList)) {
      try {
        const attachment = await readAttachment(file);
        const uploaded = await uploadProjectFile(activeProject.project_id, attachment);
        const persistedAttachment = { ...attachment, file_id: uploaded.file.file_id };
        next.push(persistedAttachment);
        nextFileIDs.push(uploaded.file.file_id);
        nextSourceFiles.push(persistedAttachment);
      } catch (error) {
        pushAgentMessage(error instanceof Error ? error.message : String(error));
      }
    }
    setAttachments((current) => [...current, ...next]);
    setAttachmentFileIDs((current) => [...current, ...nextFileIDs]);
    setSourceFiles((current) => [...current, ...nextSourceFiles]);
    const uploadedText = sourceTextFromAttachments(next);
    if (uploadedText) {
      setSourcePreviewText((current) => (current.trim() ? `${current.trim()}\n\n${uploadedText}` : uploadedText));
    }
    if (fileInputRef.current) fileInputRef.current.value = "";
  }

  async function removeAttachment(index: number) {
    const target = attachments[index];
    const fileID = target?.file_id || attachmentFileIDs[index];
    try {
      if (fileID) await deleteProjectFile(fileID);
    } catch (error) {
      pushAgentMessage(`附件未能删除，已保留在当前作品中。${error instanceof Error ? ` ${error.message}` : ""}`.trim());
      return;
    }
    setAttachments((current) => current.filter((attachment, itemIndex) => fileID ? attachment.file_id !== fileID : itemIndex !== index));
    setAttachmentFileIDs((current) => current.filter((currentFileID, itemIndex) => fileID ? currentFileID !== fileID : itemIndex !== index));
    if (fileID) setSourceFiles((current) => current.filter((file) => file.file_id !== fileID));
  }

  function resetWorkspace() {
    setRun(null);
    setEvents([]);
    setArtifacts([]);
    setAttachments([]);
    setAttachmentFileIDs([]);
    setSourceFiles([]);
    setProject(null);
    projectIDRef.current = "";
    workspacePersistenceProjectIDRef.current = "";
    setApprovalRequest(null);
    setInputText("");
    setSourcePreviewText("");
    setComposerText("");
    setView("script");
    setSelectedArtifactType(null);
    setSelectionContext(null);
    setArtifactEditState(null);
    artifactEditStateRef.current = null;
    focusedApprovalIDRef.current = null;
    setMessages([]);
    setProjectPickerOpen(true);
  }

  function applyArtifactUpdate(payload: ArtifactUpdateResponse) {
    setArtifactSyncNotice("");
    const incoming = [payload.artifact, ...(payload.artifacts || [])].filter((artifact): artifact is Artifact => Boolean(artifact));
    if (incoming.length) {
      setArtifacts((current) => mergeArtifacts(current, incoming));
    }
    if (payload.events) setEvents((current) => mergeEvents(current, payload.events || []));
    if (run?.run_id) {
      const activeProjectID = project?.project_id || "";
      void getRunSnapshot(run.run_id).then((snapshot) => {
        if (projectIDRef.current !== activeProjectID || snapshot.run.project_id !== activeProjectID) return;
        setRun(snapshot.run);
        setApprovalRequest(snapshot.approval_request || null);
        if (snapshot.events) setEvents((current) => mergeEvents(current, snapshot.events || []));
        if (snapshot.artifacts) setArtifacts((current) => mergeArtifacts(current, snapshot.artifacts || []));
        if (snapshot.run.status === "waiting_approval") focusApproval(snapshot.approval_request);
        setArtifactSyncNotice("");
      }).catch((error) => setArtifactSyncNotice(`内容已经保存，但运行状态同步失败：${error instanceof Error ? error.message : String(error)}。请重新同步。`));
    }
  }

  async function retryArtifactStateSync() {
    if (!run?.run_id) return;
    try {
      const snapshot = await getRunSnapshot(run.run_id);
      if (snapshot.run.project_id !== projectIDRef.current) return;
      setRun(snapshot.run);
      setApprovalRequest(snapshot.approval_request || null);
      if (snapshot.events) setEvents((current) => mergeEvents(current, snapshot.events || []));
      if (snapshot.artifacts) setArtifacts((current) => mergeArtifacts(current, snapshot.artifacts || []));
      setArtifactSyncNotice("");
    } catch (error) {
      setArtifactSyncNotice(`仍无法同步运行状态：${error instanceof Error ? error.message : String(error)}。内容保存结果不受影响。`);
    }
  }

  function applyArtifactConflict(currentArtifact: Artifact) {
    setArtifacts((current) => mergeArtifacts(current, [currentArtifact]));
    setArtifactSyncNotice(`检测到当前产物已有 v${currentArtifact.version} 新版本，已加载最新内容。刚才基于旧版本的修改未保存，请重新检查后再编辑。`);
  }

  function setPaneWidth(pane: ResizablePane, nextWidth: number) {
    if (pane === "sidebar") setSidebarWidth(adjustPaneWidth(pane, nextWidth, 0));
    else setAgentWidth(adjustPaneWidth(pane, nextWidth, 0));
  }

  function startPaneResize(pane: ResizablePane, event: ReactPointerEvent<HTMLDivElement>) {
    event.preventDefault();
    const startX = event.clientX;
    const startWidth = pane === "sidebar" ? sidebarWidth : agentWidth;
    const direction = pane === "sidebar" ? 1 : -1;
    setResizingPane(pane);

    function move(moveEvent: PointerEvent) {
      setPaneWidth(pane, startWidth + (moveEvent.clientX - startX) * direction);
    }

    function finish() {
      setResizingPane(null);
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", finish);
      window.removeEventListener("pointercancel", finish);
    }

    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", finish);
    window.addEventListener("pointercancel", finish);
  }

  function resizePaneWithKeyboard(pane: ResizablePane, event: ReactKeyboardEvent<HTMLDivElement>) {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    event.preventDefault();
    const direction = pane === "sidebar" ? 1 : -1;
    const delta = (event.key === "ArrowRight" ? 1 : -1) * direction * (event.shiftKey ? 32 : 8);
    if (pane === "sidebar") setSidebarWidth((current) => adjustPaneWidth(pane, current, delta));
    else setAgentWidth((current) => adjustPaneWidth(pane, current, delta));
  }

  const navItems = buildNavItems(effectiveMode, latestByType, events, run);
  const hasProcessArtifacts = Array.from(latestByType.keys()).some((type) => !["source_input", "script_context", "script_unit", "scripts"].includes(type));
  const hasPendingGenerationConfig = messages.some((message) => Boolean(message.generationConfigPrompt));
  const hasActiveRun = Boolean(run && ["running", "paused", "waiting_approval"].includes(run.status));
  const showGenerateShortcut = Boolean(project)
    && !hasActiveRun
    && !hasPendingGenerationConfig
    && !selectionContext
    && !composerText.trim()
    && !isThinking;
  const workspaceStyle = {
    "--sidebar-width": `${sidebarWidth}px`,
    "--agent-width": `${agentWidth}px`,
  } as CSSProperties;

  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-workspace-content">跳到主要内容</a>
      <header className="topbar">
        <div className="brand">
          <span className="brand-mark">N</span>
          <span className="brand-copy">
            <strong>Novel2Script</strong>
            <small>编剧工作台</small>
          </span>
        </div>
        <div className="workspace-pane-switcher" role="tablist" aria-label="工作区区域">
          <button aria-selected={activeWorkspacePane === "navigation"} className={activeWorkspacePane === "navigation" ? "active" : ""} onClick={() => {
            setSidebarOpen(true);
            setActiveWorkspacePane("navigation");
          }} role="tab" type="button">
            <PanelLeft size={15} />
            <span>目录</span>
          </button>
          <button aria-selected={activeWorkspacePane === "content"} className={activeWorkspacePane === "content" ? "active" : ""} onClick={() => setActiveWorkspacePane("content")} role="tab" type="button">
            <FileText size={15} />
            <span>内容</span>
          </button>
          <button aria-selected={activeWorkspacePane === "agent"} className={activeWorkspacePane === "agent" ? "active" : ""} onClick={() => {
            setAgentOpen(true);
            setActiveWorkspacePane("agent");
          }} role="tab" type="button">
            <Bot size={15} />
            <span>Agent</span>
            {approvalRequest ? <span className="workspace-pane-alert" aria-label="待确认" title="有内容等待确认" /> : null}
          </button>
        </div>
        <div className="topbar-actions">
          <button aria-label="打开作品中心" className="project-topbar-trigger" onClick={() => {
            setProjectPickerError("");
            setProjectPickerOpen(true);
          }} title="切换作品" type="button">
            <FolderKanban size={16} />
            <span>{project?.title || "作品中心"}</span>
          </button>
          {backendError ? <span className="topbar-meta">{backendStatus}</span> : null}
        </div>
      </header>

      <div className={`workspace pane-${activeWorkspacePane} ${sidebarOpen ? "" : "sidebar-collapsed"} ${agentOpen ? "" : "agent-collapsed"} ${resizingPane ? `resizing-${resizingPane}` : ""}`} style={workspaceStyle}>
        {!sidebarOpen ? (
          <button aria-controls="project-sidebar" aria-expanded="false" aria-label="打开目录面板" className="workspace-edge-restore workspace-edge-restore-left" onClick={() => setSidebarOpen(true)} title="打开目录面板" type="button">
            <PanelLeftOpen size={17} />
            <span>目录</span>
          </button>
        ) : null}
        <ProjectSidebar
          navItems={navItems}
          project={project}
          onSelect={(nextView, artifactType) => {
            requestNavigation({ view: nextView, artifactType });
          }}
          onCollapse={() => {
            setSidebarOpen(false);
            setActiveWorkspacePane("content");
          }}
          onOpenProjects={() => {
            setProjectPickerError("");
            setProjectPickerOpen(true);
          }}
          run={run}
          selectedArtifactType={selectedArtifactType}
          view={view}
        />

        <div
          aria-label="调整目录宽度"
          aria-orientation="vertical"
          aria-valuemax={360}
          aria-valuemin={200}
          aria-valuenow={sidebarWidth}
          className="workspace-pane-resizer workspace-pane-resizer-left"
          onDoubleClick={() => setSidebarWidth(DEFAULT_PANE_WIDTHS.sidebar)}
          onKeyDown={(event) => resizePaneWithKeyboard("sidebar", event)}
          onPointerDown={(event) => startPaneResize("sidebar", event)}
          role="separator"
          tabIndex={0}
          title="拖动调整目录宽度，双击恢复默认"
        />

        <main className="artifact-pane">
          <div className="pane-head artifact-head">
            <div>
              <h1>{mainTitle(view, selectedArtifactType)}</h1>
              <p>{mainSubtitle(view, selectedArtifactType, artifacts, run)}</p>
            </div>
            <div className="view-tabs" role="tablist" aria-label="artifact view">
              {(["artifacts", "script", "events"] as ViewMode[]).map((tab) => (
                <Button
                  className={view === tab ? "active" : ""}
                  key={tab}
                  onClick={() => {
                    requestNavigation({
                      view: tab,
                      artifactType: tab === "artifacts" ? selectedArtifactType || visibleTypes.find((type) => latestByType.has(type)) || null : null,
                    });
                  }}
                  size="sm"
                  variant="soft"
                >
                  {tab === "artifacts" && !hasProcessArtifacts ? "输入材料" : tabLabel(tab)}
                </Button>
              ))}
            </div>
          </div>
          <section
            className={`artifact-body ${view === "artifacts" && selectedArtifactType && selectedArtifactType !== "source_input" ? "artifact-body-process" : ""}`}
            id="main-workspace-content"
            onScroll={(event) => {
              if (!project?.project_id) return;
              window.localStorage.setItem(`n2s.workspace.${project.project_id}.scrollTop`, String(event.currentTarget.scrollTop));
            }}
            ref={artifactBodyRef}
          >
            {artifactSyncNotice ? (
              <div className="artifact-sync-notice" role="status">
                <span>{artifactSyncNotice}</span>
                {run?.run_id ? <button aria-label="重新同步运行状态" onClick={() => void retryArtifactStateSync()} type="button">重新同步</button> : null}
                <button aria-label="关闭版本提示" onClick={() => setArtifactSyncNotice("")} type="button">关闭</button>
              </div>
            ) : null}
            <ErrorBoundary resetKey={`${project?.project_id || "none"}:${run?.run_id || "none"}:${view}:${selectedArtifactType || ""}`} title="内容区加载失败"><ArtifactWorkspace
              artifacts={artifacts}
              events={events}
              inputText={inputText}
              latestByType={latestByType}
              editDiscardRequest={editDiscardRequest}
              editSaveRequest={editSaveRequest}
              onArtifactSelect={(artifactType) => requestNavigation({ view: "artifacts", artifactType })}
              onArtifactUpdated={applyArtifactUpdate}
              onArtifactConflict={applyArtifactConflict}
              onEditSaveComplete={finishPendingNavigation}
              onEditStateChange={setArtifactEditState}
              selectedArtifactType={selectedArtifactType}
              sourcePreviewText={sourcePreviewText}
              sourceFiles={sourceFiles}
              onSelectionChange={setSelectionContext}
              setInputText={setInputText}
              visibleTypes={visibleTypes}
              view={view}
			  editingLocked={run?.status === "running"}
              exportLocked={run?.status === "pending" || run?.status === "running"}
              projectTitle={project?.title || "未命名作品"}
              targetEpisodeCount={targetEpisodeCount(run, latestByType)}
            /></ErrorBoundary>
          </section>
        </main>

        <div
          aria-label="调整 Agent 宽度"
          aria-orientation="vertical"
          aria-valuemax={560}
          aria-valuemin={320}
          aria-valuenow={agentWidth}
          className="workspace-pane-resizer workspace-pane-resizer-right"
          onDoubleClick={() => setAgentWidth(DEFAULT_PANE_WIDTHS.agent)}
          onKeyDown={(event) => resizePaneWithKeyboard("agent", event)}
          onPointerDown={(event) => startPaneResize("agent", event)}
          role="separator"
          tabIndex={0}
          title="拖动调整 Agent 宽度，双击恢复默认"
        />

        {!agentOpen ? (
          <button aria-controls="agent-panel" aria-expanded="false" aria-label={approvalRequest ? "打开 Agent 面板，有内容等待确认" : "打开 Agent 面板"} className={`workspace-edge-restore workspace-edge-restore-right ${approvalRequest ? "attention" : ""}`} onClick={() => setAgentOpen(true)} title={approvalRequest ? "打开 Agent 面板，有内容等待确认" : "打开 Agent 面板"} type="button">
            <PanelRightOpen size={17} />
            <span>Agent</span>
            {approvalRequest ? <i aria-hidden className="workspace-edge-alert" /> : null}
          </button>
        ) : null}
        <ErrorBoundary resetKey={`${project?.project_id || "none"}:${messages.length}`} title="Agent 区域加载失败"><AgentPanel
          activeApprovalID={approvalRequest?.approval_request_id || null}
          attachments={attachments}
          composerText={composerText}
          fileInputRef={fileInputRef}
          isThinking={isThinking}
          isSendDisabled={isThinking || run?.status === "running"}
          messages={messages}
          selectionContext={selectionContext}
          showGenerateShortcut={showGenerateShortcut}
          onAddFiles={(files) => void addFiles(files)}
          onApprove={() => void resolveCurrentApproval("approve")}
          onKeepDownstream={() => void resolveCurrentApproval("keep_downstream")}
          onGenerateShortcut={() => void submitToAgent("生成剧本", undefined, undefined, undefined, undefined, true)}
          onClearSelection={() => setSelectionContext(null)}
          onComposerChange={setComposerText}
          onOpenEvents={() => {
            requestNavigation({ view: "events", artifactType: null });
          }}
          onCollapse={() => {
            setAgentOpen(false);
            setActiveWorkspacePane("content");
          }}
          onPause={() => void resolveCurrentApproval("pause")}
          onStop={() => void setRunningPaused(true)}
          onResume={() => void setRunningPaused(false)}
          onRegenerateDownstream={() => void resolveCurrentApproval("regenerate_downstream")}
          onRemoveAttachment={removeAttachment}
          onReset={requestWorkspaceReset}
          onSend={() => void submitToAgent()}
          onSourceModeChange={setSourceMode}
          onSubmitGenerationConfig={(messageID, originalMessage, config, promptSourceMode, fileIDs) => void submitGenerationConfig(messageID, originalMessage, config, promptSourceMode, fileIDs)}
          runStatus={run?.status}
          sourceMode={sourceMode}
          timelineRef={timelineRef}
        /></ErrorBoundary>
      </div>
      {pendingNavigation ? (
        <div className="unsaved-dialog-backdrop" role="presentation">
          <section aria-describedby="unsaved-dialog-description" aria-labelledby="unsaved-dialog-title" aria-modal="true" className="unsaved-dialog" role="dialog">
            <h2 id="unsaved-dialog-title">当前修改尚未保存</h2>
            <p id="unsaved-dialog-description">保存会生成当前产物的新版本；已有后续内容不会被立即覆盖。你也可以放弃本次修改，或继续留在当前页面。</p>
            <div className="unsaved-dialog-actions">
              <Button disabled={navigationSavePending} onClick={() => {
                setNavigationSavePending(true);
                setEditSaveRequest((current) => current + 1);
              }} variant="primary">{navigationSavePending ? "保存中" : "保存后继续"}</Button>
              <Button disabled={navigationSavePending} onClick={discardAndContinue}>放弃修改</Button>
              <Button disabled={navigationSavePending} onClick={() => setPendingNavigation(null)} variant="ghost">继续编辑</Button>
            </div>
          </section>
        </div>
      ) : null}
      {projectPickerOpen ? (
        <ProjectPicker
          activeProjectID={project?.project_id}
          busy={projectPickerBusy}
          error={projectPickerError}
          onClose={project ? () => setProjectPickerOpen(false) : undefined}
          onCreate={createProjectWorkspace}
          onDelete={deleteProjectWorkspace}
          onOpen={openProjectWorkspace}
          projects={projects}
        />
      ) : null}
    </div>
  );
}


