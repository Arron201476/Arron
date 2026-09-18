import { useEffect, useMemo, useRef, useState } from "react";
import { ArrowLeft, Check, CircleAlert, LoaderCircle, PackageOpen, Power, RefreshCw, ShieldCheck, ShieldOff, Terminal, Trash2, Upload } from "lucide-react";
import { api, ApiError } from "../../api";
import { createUUID } from "../../uuid";
import { validateSkillLifecycleResult, type SkillLifecycleRequest } from "../../skillLifecycle";
import { skillArchiveHash, skillPackageLabel, skillPackageScope, validateSkillPackageResult, type SkillPackageInput, type SkillPackageRequest } from "../../skillPackage";
import { skillMutationJournal, SkillJournalError, type SkillMutationRequest } from "../../skillMutationJournal";
import { trustedComposerViewKey, trustedConfigViewKey } from "../../workspaceProjection";
import type { ComposerRegistryEntry, Principal, Project, PublicSkill, ScriptSandboxPolicy, SkillDirectoryUpdate, SkillInstallAttempt, SkillInstallation, SkillInstallTarget } from "../../types";
import { AgentToolInventory } from "./AgentToolInventory";

export function SkillManagement({ workspaceName, role = "owner", userID, workspaceID }: { workspaceName: string; role?: Principal["role"]; userID: string; workspaceID: string }) {
  const journalOwner = { workspaceID, userID };
  const canAdmin = role === "owner" || role === "admin";
  const canEdit = canAdmin || role === "editor";
  const [installScope, setInstallScope] = useState<SkillInstallTarget["scope"]>(canAdmin ? "workspace" : "user");
  const [projects, setProjects] = useState<Project[]>([]);
  const [projectID, setProjectID] = useState("");
  const [supportedScopes, setSupportedScopes] = useState<SkillInstallTarget["scope"][]>([]);
  const [supportsDirectoryUpdates, setSupportsDirectoryUpdates] = useState(false);
  const [supportsLifecycle, setSupportsLifecycle] = useState(false);
  const [supportsPackages, setSupportsPackages] = useState(false);
  const [directoryUpdate, setDirectoryUpdate] = useState<SkillDirectoryUpdate | null>(null);
  const projectContextID = installScope === "project" ? projectID : "";
  const installTarget: SkillInstallTarget = { scope: installScope, ...(projectContextID ? { project_id: projectContextID } : {}) };
  const [hasSnapshot, setHasSnapshot] = useState(false);
  const ready = useRef(false);
  const running = useRef(false);
  const mutationInFlight = useRef(false);
  const mounted = useRef(false);
  const contextKey = JSON.stringify([workspaceName, role, projectContextID, userID, workspaceID]);
  const context = useRef({ key: contextKey, generation: 0 });
  if (context.current.key !== contextKey) {
    context.current = { key: contextKey, generation: context.current.generation + 1 };
    ready.current = false;
  }
  const canInstall = hasSnapshot && ready.current && supportsPackages && canEdit && supportedScopes.includes(installScope) && (installScope !== "workspace" || canAdmin) && (installScope !== "project" || Boolean(projectContextID));
  const loadSequence = useRef(0);
  const [items, setItems] = useState<SkillInstallation[]>([]);
  const [attempts, setAttempts] = useState<SkillInstallAttempt[]>([]);
  const [composer, setComposer] = useState<ComposerRegistryEntry[]>([]);
  const [scriptPolicy, setScriptPolicy] = useState<ScriptSandboxPolicy | null>(null);
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [working, setBusy] = useState("");
  const pending = useRef<SkillMutationRequest | null>(null);
  const persistedKey = useRef<string | null>(null);
  const settledKey = useRef<string | null>(null);
  const [needsJournalCleanup, setNeedsJournalCleanup] = useState(false);
  const [pendingMutation, setPendingMutation] = useState<SkillMutationRequest | null>(null);
  const [mutationNotice, setMutationNotice] = useState("");
  const busy = working || (pendingMutation ? "pending-mutation" : "");
  const [failure, setFailure] = useState("");
  const [refreshNotice, setRefreshNotice] = useState("");
  const [loading, setLoading] = useState(true);
  const [scanDiagnostics, setScanDiagnostics] = useState<Array<{ code: string; message: string; path: string }>>([]);
  const installInput = useRef<HTMLInputElement>(null);
  const upgradeInput = useRef<HTMLInputElement>(null);
  const uploadSelection = useRef<{ mode: "install" | "upgrade"; generation: number; target: string } | null>(null);

  const load = async () => {
    const generation = context.current.generation;
    const sequence = ++loadSequence.current;
    const current = () => mounted.current && generation === context.current.generation && sequence === loadSequence.current;
    ready.current = false;
    setHasSnapshot(false);
    setFailure("");
    setLoading(true);
    try {
      const [installations, installAttempts, projection, sandboxPolicy, projectItems, options, recovery] = await Promise.all([
        api.listSkills(true), api.listSkillInstallAttempts(), projectContextID ? api.getProjectWorkspaceProjection(projectContextID) : api.getWorkspaceProjection(), api.getScriptSandboxPolicy().catch(() => null), api.listProjects(), api.getSkillManagementOptions().catch((error: unknown) => { if (error instanceof ApiError && error.status === 404) return { install_scopes: ["workspace"] as SkillInstallTarget["scope"][], directory_updates: false, lifecycle_commands: false, package_commands: false }; throw error; }),
        skillMutationJournal.read(journalOwner).then((request) => ({ request, error: null as unknown })).catch((error: unknown) => ({ request: null, error })),
      ]);
      if (!current()) return false;
      setProjects(projectItems);
      setSupportedScopes(options.install_scopes);
      setSupportsDirectoryUpdates(Boolean(options.directory_updates));
      setSupportsLifecycle(Boolean(options.lifecycle_commands));
      setSupportsPackages(Boolean(options.package_commands));
      setItems(installations);
      setAttempts(installAttempts);
      setComposer(projection.registries.composer);
      setScriptPolicy(sandboxPolicy);
      if (recovery.error) setFailure(errorMessage(recovery.error));
      else if (!mutationInFlight.current || !pending.current) {
        pending.current = recovery.request; setPendingMutation(recovery.request);
        persistedKey.current = recovery.request?.key ?? null;
        if (settledKey.current !== recovery.request?.key) { settledKey.current = null; setNeedsJournalCleanup(false); }
      } else if (recovery.request?.key === pending.current.key) {
        persistedKey.current = recovery.request.key;
        pending.current.hadUnknownResult = true;
      }
      setSelectedID((current) => installations.some((item) => item.skill_installation_id === current) || projection.registries.composer.some((entry) => `registry:${entry.capability_id}` === current)
        ? current
        : installations.find((item) => item.status !== "uninstalled")?.skill_installation_id ?? (projection.registries.composer[0] ? `registry:${projection.registries.composer[0].capability_id}` : installations[0]?.skill_installation_id ?? null));
      ready.current = !recovery.error;
      setHasSnapshot(!recovery.error);
      setRefreshNotice("");
      return !recovery.error;
    } catch (error) {
      if (current()) { setFailure(errorMessage(error)); setDirectoryUpdate(null); }
      return false;
    } finally {
      if (current()) setLoading(false);
    }
  };
  useEffect(() => {
    mounted.current = true;
    void load();
    return () => { mounted.current = false; ready.current = false; loadSequence.current++; };
  }, [contextKey]);
  useEffect(() => {
    if (!pendingMutation) return;
    const beforeUnload = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", beforeUnload);
    return () => window.removeEventListener("beforeunload", beforeUnload);
  }, [pendingMutation]);

  const selected = items.find((item) => item.skill_installation_id === selectedID) ?? null;
  const canManageSelected = hasSnapshot && ready.current && canEdit && (selected?.scope !== "workspace" || canAdmin);
  const discovered = composer.filter((entry) => !items.some((item) => item.status !== "uninstalled" && item.capability_id === entry.capability_id && (!entry.skill || item.scope === entry.skill.scope) && item.versions.some((version) => version.skill_version_id === item.active_version_id && (!entry.skill || version.content_hash === entry.skill.content_hash))));
  const selectedDiscovered = discovered.find((entry) => `registry:${entry.capability_id}` === selectedID) ?? null;
  const activeVersion = selected?.versions.find((version) => version.skill_version_id === selected.active_version_id) ?? null;
  const projection = composer.find((entry) => entry.capability_id === selected?.capability_id && (!entry.skill || (entry.skill.scope === selected.scope && entry.skill.content_hash === activeVersion?.content_hash))) ?? null;
  const relatedAttempts = useMemo(() => attempts.filter((attempt) => !selected || ((!attempt.scope || (attempt.scope === selected.scope && attempt.scope_ref === selected.scope_ref)) && selected.versions.some((version) => version.source_name === attempt.source_name))).slice(0, 8), [attempts, selected]);
  const scripts = projection?.skill?.scripts ?? activeVersion?.manifest.scripts ?? [];
  const selectedDirectoryUpdate = directoryUpdate?.skill_installation_id === selected?.skill_installation_id ? directoryUpdate : null;
  useEffect(() => { setDirectoryUpdate(null); }, [selectedID, activeVersion?.skill_version_id]);
  const uploadTargetKey = (mode: "install" | "upgrade") => JSON.stringify(mode === "install" ? installTarget : [selected?.skill_installation_id, selected?.active_version_id, selected?.enabled, selected?.status, selected?.updated_at, selected?.events.length]);
  const chooseUpload = (mode: "install" | "upgrade") => {
    if (running.current || pending.current || !ready.current || !supportsPackages || (mode === "install" ? !canInstall : !canManageSelected || selected?.status === "uninstalled")) return;
    uploadSelection.current = { mode, generation: context.current.generation, target: uploadTargetKey(mode) };
    (mode === "install" ? installInput : upgradeInput).current?.click();
  };

  const perform = async (key: string, operation: (current: () => boolean) => Promise<unknown>, lifecycle?: SkillMutationRequest) => {
    const cleanupOnly = lifecycle && settledKey.current === lifecycle.key;
    if (running.current || !mounted.current || (!cleanupOnly && !canEdit) || (lifecycle ? pending.current !== lifecycle : !ready.current || pending.current)) return;
    running.current = true;
    mutationInFlight.current = Boolean(lifecycle);
    const generation = context.current.generation;
    const current = () => mounted.current && generation === context.current.generation;
    setBusy(key); setFailure(""); setRefreshNotice("");
    if (!lifecycle) setMutationNotice("");
    try {
      if (lifecycle && settledKey.current === lifecycle.key) {
        await clearSavedMutation(lifecycle, current);
      } else {
        if (lifecycle) {
          if (persistedKey.current === lifecycle.key) await skillMutationJournal.requireCurrent(journalOwner, lifecycle);
          else {
            const saved = await skillMutationJournal.claim(journalOwner, lifecycle);
            if (!current()) return;
            persistedKey.current = saved.request.key;
            if (!saved.accepted) {
              pending.current = saved.request; setPendingMutation(saved.request);
              setFailure("已有待确认的原操作，未提交本次新操作。");
              return;
            }
          }
        }
        if (!current()) return;
        await operation(current);
      }
      if (!current()) return;
      const loaded = await load();
      if (current() && !loaded && !ready.current) setRefreshNotice(key === "directory-check" || key === "refresh" ? "目录状态尚未刷新。" : "操作已提交，但最新目录尚未读取。请刷新状态，不要重复提交。");
    } catch (error) {
      if (current()) {
        ready.current = false; setHasSnapshot(false); setDirectoryUpdate(null); setFailure(errorMessage(error));
        if (lifecycle) {
          const rejected = error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS" && error.code !== "IDEMPOTENCY_RESULT_INVALID";
          const permissionLost = lifecycle.hadUnknownResult && error instanceof ApiError && (error.status === 403 || error.status === 404);
          if (rejected && !permissionLost) {
            if (persistedKey.current === lifecycle.key) {
              settledKey.current = lifecycle.key; setNeedsJournalCleanup(true);
              try { await clearSavedMutation(lifecycle, current); }
              catch (cleanupError) { if (current()) setFailure(errorMessage(cleanupError)); }
            } else { pending.current = null; setPendingMutation(null); }
          }
          else lifecycle.hadUnknownResult = true;
        }
      }
    } finally {
      running.current = false;
      mutationInFlight.current = false;
      if (mounted.current) setBusy("");
    }
  };
  const clearSavedMutation = async (command: SkillMutationRequest, current: () => boolean) => {
    const next = await skillMutationJournal.settle(journalOwner, command);
    if (!current()) return;
    pending.current = next; setPendingMutation(next); persistedKey.current = next?.key ?? null;
    settledKey.current = null; setNeedsJournalCleanup(false);
  };
  const executeMutation = async (command: SkillLifecycleRequest) => {
    if (settledKey.current !== command.key && (!canEdit || command.installation.scope === "workspace" && !canAdmin || !supportsLifecycle)) return;
    await perform(command.action, async (current) => {
      const result = command.action === "activate" ? await api.activateSkillVersion(command.installation, command.version!, command.key)
        : command.action === "uninstall" ? await api.uninstallSkill(command.installation, command.key)
          : await api.setSkillEnabled(command.installation, command.action === "enable", command.key);
      if (!current()) return;
      validateSkillLifecycleResult(result, command, userID);
      settledKey.current = command.key; setNeedsJournalCleanup(true);
      setMutationNotice(result.installation.events.length > command.installation.events.length + 1 ? "原操作已完成，Skill 状态此后已有变化。" : "原操作已完成。");
      await clearSavedMutation(command, current);
    }, command);
  };
  const executePackage = async (command: SkillPackageRequest) => {
    if (settledKey.current !== command.key && (!canEdit || skillPackageScope(command) === "workspace" && !canAdmin || !supportsPackages)) return;
    await perform(command.action, async (current) => {
      const sourceHash = "file" in command ? await skillArchiveHash(command.file) : "";
      if (!current()) return;
      const result = command.action === "install_zip" ? await api.installSkill(command.file, command.target, command.key)
        : command.action === "upgrade_zip" ? await api.upgradeSkill(command.installation, command.file, command.key)
          : command.action === "adopt_directory" ? await api.installDiscoveredSkill(command.capabilityID, command.version, command.contentHash, command.target, command.key)
            : await api.updateSkillFromDirectory(command.preview, command.installation, command.key);
      if (!current()) return;
      validateSkillPackageResult(result, command, sourceHash, userID, workspaceID);
      settledKey.current = command.key; setNeedsJournalCleanup(true); setDirectoryUpdate(null);
      setSelectedID(result.installation.skill_installation_id);
      setMutationNotice(result.installation.active_version_id !== result.receipt.skill_version_id || !result.installation.enabled || result.installation.status !== "installed" ? "原安装已完成，该版本当前未启用。" : "原安装已完成。");
      await clearSavedMutation(command, current);
    }, command);
  };
  const beginPackage = (input: SkillPackageInput) => {
    if (running.current || pending.current || !ready.current || !supportsPackages || ("installation" in input ? !canManageSelected : !canInstall)) return;
    // File objects are immutable; retain the original bytes without cloning DOM objects.
    const command = { ...input, ...("installation" in input ? { installation: structuredClone(input.installation) } : { target: { ...input.target } }), package: true, key: createUUID() } as SkillPackageRequest;
    pending.current = command; setPendingMutation(command); setMutationNotice("");
    void executePackage(command);
  };
  const mutate = (action: SkillLifecycleRequest["action"], version?: string) => {
    if (running.current || pending.current || !selected || !canManageSelected || !supportsLifecycle || selected.status === "uninstalled") return;
    if (action === "uninstall" && !window.confirm(`卸载 ${selected.skill_name}？历史版本和审计记录会保留。`)) return;
    const command: SkillLifecycleRequest = { installation: structuredClone(selected), action, version, key: createUUID() };
    pending.current = command; setPendingMutation(command); setMutationNotice("");
    void executeMutation(command);
  };
  const upload = (file: File | undefined, mode: "install" | "upgrade") => {
    const selection = uploadSelection.current;
    uploadSelection.current = null;
    if (!file) return;
    if (!selection || selection.mode !== mode || selection.generation !== context.current.generation || selection.target !== uploadTargetKey(mode)) { setFailure("安装目标或版本已变化，请重新选择安装包。"); return; }
    if (!file.name.toLowerCase().endsWith(".zip")) { setFailure("Skill 安装包必须是 ZIP 文件。"); return; }
    if (mode === "upgrade" && !selected) return;
    if ((mode === "install" && !canInstall) || (mode === "upgrade" && !canManageSelected)) return;
    beginPackage(mode === "install" ? { action: "install_zip", file, target: installTarget } : { action: "upgrade_zip", file, installation: selected! });
  };
  const toggleScriptPolicy = () => {
    if (!scriptPolicy || !canAdmin || running.current || !ready.current) return;
    if (!scriptPolicy.enabled && !window.confirm("启用工作区 Skill 脚本执行？每次运行仍需用户确认。")) return;
    void perform("script-policy", () => api.updateScriptSandboxPolicy(scriptPolicy, !scriptPolicy.enabled));
  };
  const reload = async () => {
    if (running.current) return;
    running.current = true;
    setBusy("reload");
    try { await load(); }
    finally { running.current = false; if (mounted.current) setBusy(""); }
  };

  return <div className="skills-page">
    <header className="skills-header">
      <a href="/" className="back-button"><ArrowLeft size={16} />作品</a>
      <div><strong>Skill 管理</strong><span>{workspaceName}</span></div>
      <a className="header-link" href="/instructions">Agent 规则</a>
      <div className="skills-scope-controls">
        <label>安装范围<select aria-label="安装范围" value={installScope} disabled={Boolean(busy) || !canEdit || loading} onChange={(event) => setInstallScope(event.target.value as SkillInstallTarget["scope"])}><option value="user" disabled={!supportedScopes.includes("user")}>个人</option><option value="project" disabled={!supportedScopes.includes("project")}>项目</option>{canAdmin && <option value="workspace" disabled={!supportedScopes.includes("workspace")}>工作区</option>}</select></label>
        {installScope === "project" && <label>项目<select aria-label="安装项目" value={projectID} disabled={Boolean(busy)} onChange={(event) => setProjectID(event.target.value)}><option value="">选择项目</option>{projects.map((project) => <option key={project.project_id} value={project.project_id}>{project.title}</option>)}</select></label>}
      </div>
      <input ref={installInput} className="visually-hidden" type="file" accept=".zip,application/zip" onChange={(event) => { upload(event.target.files?.[0], "install"); event.currentTarget.value = ""; }} />
      <button className="primary-button" disabled={Boolean(busy) || loading || !canInstall} onClick={() => chooseUpload("install")}>{busy === "install_zip" ? <LoaderCircle className="spin" size={15} /> : <Upload size={15} />}安装 Skill</button>
    </header>
    <main className="skills-layout">
      <section className="skills-list" aria-label="Skill 列表" aria-busy={loading}>
        <header><div><h1>Skills</h1><p>{composer.filter((entry) => entry.status === "available").length} 个可用 · {items.filter((item) => item.status !== "uninstalled").length} 个受管理</p></div><button className="icon-button" aria-label="刷新 Skill 列表" title="重新扫描目录" disabled={Boolean(busy) || loading || !canEdit || !hasSnapshot} onClick={() => void perform("refresh", async (current) => { const result = await api.refreshSkills(projectContextID || undefined); if (current()) setScanDiagnostics(result.diagnostics ?? []); })}><RefreshCw className={loading || busy === "refresh" ? "spin" : ""} size={15} /></button></header>
        {loading && items.length === 0 && discovered.length === 0 ? <div className="skill-empty" role="status"><LoaderCircle className="spin" size={24} /><strong>正在读取 Skills…</strong></div> : items.length === 0 && discovered.length === 0 ? <div className="skill-empty"><PackageOpen size={24} /><strong>暂无 Skill</strong></div> : <div className="skill-table">{items.map((item) => {
          const active = item.versions.find((version) => version.skill_version_id === item.active_version_id);
          return <button key={item.skill_installation_id} disabled={Boolean(busy) || loading} className={item.skill_installation_id === selectedID ? "selected" : ""} onClick={() => { if (!running.current) setSelectedID(item.skill_installation_id); }}>
            <span className={`skill-state ${item.enabled && item.status === "installed" ? "available" : "inactive"}`} />
            <span><strong>{item.skill_name}</strong><small>{item.capability_id}</small></span>
            <span><strong title={active?.version}>{active?.version ?? "-"}</strong><small>{scopeLabel(item.scope)}</small></span>
            <em>{statusLabel(item)}</em>
          </button>;
        })}{discovered.map((entry) => <button key={`registry:${entry.capability_id}`} disabled={Boolean(busy) || loading} className={`registry:${entry.capability_id}` === selectedID ? "selected" : ""} onClick={() => { if (!running.current) setSelectedID(`registry:${entry.capability_id}`); }}>
          <span className={`skill-state ${entry.status === "available" ? "available" : "inactive"}`} />
          <span><strong>{entry.label}</strong><small>{entry.capability_id}</small></span>
          <span><strong title={entry.version}>{entry.version}</strong><small>{entry.skill ? "本地目录" : "内置能力"}</small></span>
          <em>{entry.status === "available" ? "可用" : entry.status}</em>
        </button>)}</div>}
        {scanDiagnostics.length > 0 && <section className="skill-section skill-scan-diagnostics" aria-label="目录扫描诊断"><h3>目录扫描诊断</h3><div className="skill-diagnostics">{scanDiagnostics.map((item, index) => <div key={`${item.path}:${item.code}:${index}`}><CircleAlert size={14} /><span><strong>{item.code}</strong><small>{item.path}</small><em>{item.message}</em></span></div>)}</div></section>}
      </section>

      <section className="skill-detail" aria-label="Skill 详情">
        {selectedDiscovered ? <RegistrySkillDetail entry={selectedDiscovered} busy={Boolean(busy) || loading || !canInstall} scope={installScope} scriptPolicy={scriptPolicy} canAdmin={canAdmin} policyBusy={Boolean(busy) || loading || !hasSnapshot} onToggleScriptPolicy={toggleScriptPolicy} onInstall={() => beginPackage({ action: "adopt_directory", capabilityID: selectedDiscovered.capability_id, version: selectedDiscovered.version, contentHash: selectedDiscovered.skill!.content_hash, target: installTarget })} /> : !selected ? <div className="skill-empty"><PackageOpen size={24} /><strong>选择一个 Skill</strong></div> : <>
          <header className="skill-detail-heading"><div><span className="detail-kicker">{scopeLabel(selected.scope)}</span><h2>{activeVersion?.manifest.interface.display_name || selected.skill_name}</h2><p>{activeVersion?.manifest.interface.short_description || selected.capability_id}</p></div><span className={`detail-status ${selected.enabled ? "available" : "inactive"}`}>{statusLabel(selected)}</span></header>
          <dl className="skill-facts">
            <div><dt>Capability</dt><dd>{selected.capability_id}</dd></div>
            <div><dt>执行模式</dt><dd>{activeVersion?.execution_mode ?? "-"}</dd></div>
            <div><dt>注册状态</dt><dd>{selected.registry_status}{selected.registry_reason_code ? ` · ${selected.registry_reason_code}` : ""}</dd></div>
            <div><dt>作用域</dt><dd>{scopeLabel(selected.scope)} · {selected.scope === "project" ? projects.find((project) => project.project_id === selected.scope_ref)?.title || selected.scope_ref : selected.scope_ref}</dd></div>
            {projection?.skill && <div><dt>文件位置</dt><dd>{projection.skill.path}</dd></div>}
            <div><dt>输入视图</dt><dd>{projection ? trustedComposerViewKey(projection.view_key) : "inspector"}</dd></div>
            <div><dt>配置视图</dt><dd>{projection ? trustedConfigViewKey(projection.config.view_key) : "inspector"}</dd></div>
          </dl>

          <section className="skill-section"><div className="skill-section-title"><h3>版本</h3><input ref={upgradeInput} className="visually-hidden" type="file" accept=".zip,application/zip" onChange={(event) => { upload(event.target.files?.[0], "upgrade"); event.currentTarget.value = ""; }} /><div className="skill-version-actions">{supportsDirectoryUpdates && <button className="icon-button" title="检查目录更新" aria-label="检查目录更新" disabled={Boolean(busy) || !canManageSelected || selected.status === "uninstalled"} onClick={() => void perform("directory-check", async (current) => {
            const preview = await api.previewSkillDirectoryUpdate(selected.skill_installation_id);
            if (!current()) return;
            if (preview.skill_installation_id !== selected.skill_installation_id || preview.capability_id !== selected.capability_id || preview.current_version_id !== selected.active_version_id) throw new ApiError("SKILL_DISCOVERY_CHANGED", "目录预览与所选 Skill 或当前版本不一致，请刷新后重试。", 409);
            setDirectoryUpdate(preview);
          })}><RefreshCw size={14} className={busy === "directory-check" ? "spin" : ""} /></button>}<button className="secondary-button" disabled={Boolean(busy) || !canManageSelected || !supportsPackages || selected.status === "uninstalled"} onClick={() => chooseUpload("upgrade")}><Upload size={14} />{busy === "upgrade_zip" ? "正在校验…" : "上传新版本"}</button></div></div>
            {selectedDirectoryUpdate && <div className="skill-directory-update" aria-label="目录更新预览"><h4>目录版本</h4>{selectedDirectoryUpdate.source_path && <dl><div><dt>来源</dt><dd>{scopeLabel(selectedDirectoryUpdate.source_scope ?? "workspace")} · {selectedDirectoryUpdate.source_path}</dd></div><div><dt>版本</dt><dd>{selectedDirectoryUpdate.version}</dd></div><div><dt>SHA256</dt><dd>{selectedDirectoryUpdate.content_hash}</dd></div></dl>}{selectedDirectoryUpdate.user_message && <p className={selectedDirectoryUpdate.status === "version_conflict" ? "form-error" : "skill-muted"}>{selectedDirectoryUpdate.user_message}</p>}{selectedDirectoryUpdate.status === "available" && <button className="secondary-button" disabled={Boolean(busy) || !canManageSelected || !supportsPackages} onClick={() => beginPackage({ action: "update_directory", preview: { ...selectedDirectoryUpdate }, installation: selected })}><RefreshCw size={14} />{busy === "update_directory" ? "正在更新…" : "更新到目录版本"}</button>}</div>}
            <div className="skill-versions">{selected.versions.map((version) => {
            const active = version.skill_version_id === selected.active_version_id;
            return <div key={version.skill_version_id}><span><strong>{version.version}</strong><small>{version.content_hash.slice(0, 19)}… · {new Date(version.created_at).toLocaleDateString("zh-CN")}</small></span>{active ? <em><Check size={13} />当前版本</em> : <button className="text-action" disabled={Boolean(busy) || !canManageSelected || !supportsLifecycle || selected.status === "uninstalled"} onClick={() => mutate("activate", version.version)}>{busy === "activate" ? "切换中…" : "设为当前"}</button>}</div>;
          })}</div></section>

          <section className="skill-section"><h3>依赖</h3>{(activeVersion?.manifest.dependencies ?? []).length ? <div className="skill-dependencies">{activeVersion!.manifest.dependencies.map((dependency) => <div key={`${dependency.type}:${dependency.value}`}><span className={dependency.status === "available" ? "ok" : "warning"}>{dependency.status === "available" ? <Check size={13} /> : <CircleAlert size={13} />}</span><span><strong>{dependency.value}</strong><small>{dependency.description || `${dependency.type}${dependency.transport ? ` · ${dependency.transport}` : ""}`}</small>{dependency.user_message && <em>{dependency.user_message}</em>}</span></div>)}</div> : <p className="skill-muted">没有外部工具依赖。</p>}</section>

          <SkillScriptSection scripts={scripts} policy={scriptPolicy} canAdmin={canAdmin} busy={Boolean(busy) || loading || !hasSnapshot} onToggle={toggleScriptPolicy} />

          <section className="skill-section"><h3>安装诊断</h3>{relatedAttempts.length ? <div className="skill-diagnostics">{relatedAttempts.map((attempt) => <div key={attempt.skill_install_attempt_id}><span className={attempt.status === "completed" ? "ok" : "warning"}>{attempt.status === "completed" ? <Check size={13} /> : <CircleAlert size={13} />}</span><span><strong>{attempt.status === "completed" ? "校验通过" : attempt.failure_code || attempt.status}</strong><small>{attempt.source_name} · {new Date(attempt.created_at).toLocaleString("zh-CN")}</small>{attempt.diagnostics.map((diagnostic) => <em key={diagnostic.code}>{diagnostic.code}{diagnostic.message ? ` · ${diagnostic.message}` : ""}</em>)}</span></div>)}</div> : <p className="skill-muted">没有安装诊断记录。</p>}</section>

          <footer className="skill-actions"><button className="secondary-button" disabled={Boolean(busy) || !canManageSelected || !supportsLifecycle || selected.status === "uninstalled"} onClick={() => mutate(selected.enabled ? "disable" : "enable")}><Power size={14} />{busy === "enable" || busy === "disable" ? "处理中…" : selected.enabled ? "禁用" : "启用"}</button><button className="destructive-outline" disabled={Boolean(busy) || !canManageSelected || !supportsLifecycle || selected.status === "uninstalled"} onClick={() => mutate("uninstall")}><Trash2 size={14} />{busy === "uninstall" ? "正在卸载…" : "卸载"}</button></footer>
        </>}
      </section>
    </main>
    <AgentToolInventory canManage={canAdmin} onConfigurationChange={async () => { await load(); }} />
    {mutationNotice && <p role="status">{mutationNotice}</p>}
    {pendingMutation && <div role="status"><p>{mutationLabel(pendingMutation, workspaceID, userID)}{needsJournalCleanup ? "：本地记录待清理。" : "：原操作结果待确认。"}</p><button className="secondary-button" disabled={Boolean(working) || (!needsJournalCleanup && (!("package" in pendingMutation ? supportsPackages : supportsLifecycle) || !canEdit || ("package" in pendingMutation ? skillPackageScope(pendingMutation) : pendingMutation.installation.scope) === "workspace" && !canAdmin))} onClick={() => void ("package" in pendingMutation ? executePackage(pendingMutation) : executeMutation(pendingMutation))}><RefreshCw size={14} />{needsJournalCleanup ? "清理本地记录" : "重试原操作"}</button></div>}
    {(failure || refreshNotice || pendingMutation) && <div className="skills-global-error" aria-live="polite">{refreshNotice && <p>{refreshNotice}</p>}{failure && <p>{failure}</p>}<button className="secondary-button" disabled={Boolean(working) || loading} onClick={() => void reload()}><RefreshCw size={14} />刷新状态</button></div>}
  </div>;
}

function RegistrySkillDetail({ entry, busy, scope, onInstall, scriptPolicy, canAdmin, policyBusy, onToggleScriptPolicy }: { entry: ComposerRegistryEntry; busy: boolean; scope: SkillInstallTarget["scope"]; onInstall: () => void; scriptPolicy: ScriptSandboxPolicy | null; canAdmin: boolean; policyBusy: boolean; onToggleScriptPolicy: () => void }) {
  return <>
    <header className="skill-detail-heading"><div><span className="detail-kicker">{entry.skill ? "本地目录" : "内置能力"}</span><h2>{entry.label}</h2><p>{entry.description}</p></div><span className={`detail-status ${entry.status === "available" ? "available" : "inactive"}`}>{entry.status === "available" ? "可用" : entry.status}</span></header>
    <dl className="skill-facts">
      <div><dt>Capability</dt><dd>{entry.capability_id}</dd></div>
      <div><dt>版本</dt><dd>{entry.version}</dd></div>
      <div><dt>执行模式</dt><dd>{entry.execution_mode}</dd></div>
      <div><dt>管理来源</dt><dd>{entry.skill ? "文件目录 · 非上传安装" : "平台内置"}</dd></div>
      {entry.skill && <><div><dt>文件位置</dt><dd>{entry.skill.path}</dd></div><div><dt>目录作用域</dt><dd>{entry.skill.scope}</dd></div><div><dt>自动选择</dt><dd>{entry.skill.allow_implicit_invocation ? "允许" : "仅手动选择"}</dd></div></>}
      <div><dt>输入视图</dt><dd>{trustedComposerViewKey(entry.view_key)}</dd></div>
      <div><dt>配置视图</dt><dd>{trustedConfigViewKey(entry.config.view_key)}</dd></div>
    </dl>
    {entry.user_message && <p className="form-error">{entry.user_message}</p>}
    <section className="skill-section"><h3>依赖</h3>{entry.skill?.dependencies?.length ? <div className="skill-dependencies">{entry.skill.dependencies.map((dependency) => <div key={`${dependency.type}:${dependency.value}`}><span className={dependency.status === "available" ? "ok" : "warning"}>{dependency.status === "available" ? <Check size={13} /> : <CircleAlert size={13} />}</span><span><strong>{dependency.value}</strong><small>{dependency.description || dependency.type}</small>{dependency.user_message && <em>{dependency.user_message}</em>}</span></div>)}</div> : <p className="skill-muted">没有外部工具依赖。</p>}</section>
    <SkillScriptSection scripts={entry.skill?.scripts ?? []} policy={scriptPolicy} canAdmin={canAdmin} busy={policyBusy} onToggle={onToggleScriptPolicy} />
    {entry.skill && <footer className="skill-actions"><button className="primary-button" disabled={busy} onClick={onInstall}><PackageOpen size={14} />纳入{scopeLabel(scope)}管理</button></footer>}
  </>;
}

function SkillScriptSection({ scripts, policy, canAdmin, busy, onToggle }: { scripts: NonNullable<PublicSkill["scripts"]>; policy: ScriptSandboxPolicy | null; canAdmin: boolean; busy: boolean; onToggle: () => void }) {
  return <section className="skill-section"><div className="skill-section-title"><h3>脚本沙箱</h3>{scripts.length > 0 && policy && <button className="secondary-button" disabled={busy || !canAdmin || (!policy.enabled && !policy.sandbox.available)} onClick={onToggle}>{policy.enabled ? <ShieldCheck size={14} /> : <ShieldOff size={14} />}{policy.enabled ? "已开启" : "已关闭"}</button>}</div>
    {scripts.length === 0 ? <p className="skill-muted">当前版本没有可执行脚本。</p> : <div className="skill-dependencies">{scripts.map((script) => <div key={script.id}><span className={policy?.enabled && policy.sandbox.available ? "ok" : "warning"}><Terminal size={13} /></span><span><strong>{script.path}</strong><small>{script.description} · {script.runtime}</small></span></div>)}</div>}
    {scripts.length > 0 && policy && <p className="skill-muted">{policy.sandbox.available ? `${policy.sandbox.adapter} · ${policy.sandbox.engine ?? "OCI"} · ${policy.limits.timeout_seconds}s` : policy.sandbox.user_message || policy.sandbox.reason_code}</p>}
  </section>;
}

function statusLabel(item: SkillInstallation): string {
  if (item.status === "uninstalled") return "已卸载";
  if (item.status === "broken") return "异常";
  if (item.status === "pending_runtime_support") return "等待运行时";
  if (item.enabled && item.registry_status === "shadowed") return "被更高范围覆盖";
  return item.enabled ? "已启用" : "已禁用";
}

function scopeLabel(scope: string): string {
  return ({ user: "个人", project: "项目", workspace: "工作区", system: "平台" } as Record<string, string>)[scope] ?? scope;
}

function mutationLabel(command: SkillMutationRequest, workspaceID: string, userID: string): string {
  const action = { enable: "启用", disable: "禁用", activate: "切换版本", uninstall: "卸载", install_zip: "安装 ZIP", upgrade_zip: "升级 ZIP", adopt_directory: "纳管目录", update_directory: "更新目录" }[command.action];
  const label = "package" in command ? skillPackageLabel(command) : command.installation.skill_name;
  const scope = "package" in command ? skillPackageScope(command) : command.installation.scope;
  const scopeRef = "installation" in command ? command.installation.scope_ref : command.target.project_id ?? (scope === "workspace" ? workspaceID : userID);
  return `${action}${command.action === "activate" ? ` ${command.version}` : ""} · ${label} · ${scopeLabel(scope)} · ${scopeRef}`;
}

function errorMessage(error: unknown): string {
  return error instanceof ApiError || error instanceof SkillJournalError ? error.message : "无法读取 Skill 管理状态。";
}
