import { useEffect, useRef, useState } from "react";
import { ArrowLeft, BadgeCheck, Download, PackagePlus, RefreshCw } from "lucide-react";
import { api, ApiError } from "../../api";
import { createUUID } from "../../uuid";
import type { Principal, ProjectSkillDraft, ProjectSkillInstallArguments, ProjectSkillInstallResult, SkillInstallTarget } from "../../types";

const scopeLabels = { project: "当前作品", user: "仅自己", workspace: "整个工作区" };

export function SkillDraftPanel({ projectID, root, principal, revisionKey = "", onBack, onInstalled, onPendingChange }: {
  projectID: string; root: string; principal?: Principal | null; revisionKey?: string; onBack: () => void; onInstalled?: () => void | Promise<void>; onPendingChange?: (pending: boolean) => void;
}) {
  const [preview, setPreview] = useState<ProjectSkillDraft | null>(null);
  const [scopes, setScopes] = useState<SkillInstallTarget["scope"][]>([]);
  const [selectedScope, setScope] = useState<SkillInstallTarget["scope"]>("project");
  const [checking, setChecking] = useState(true);
  const [installing, setInstalling] = useState(false);
  const [failure, setFailure] = useState("");
  const [result, setResult] = useState<ProjectSkillInstallResult | null>(null);
  const [reload, setReload] = useState(0);
  const [validatedRevision, setValidatedRevision] = useState(revisionKey);
  const [uncertain, setUncertain] = useState(false);
  const [refreshFailed, setRefreshFailed] = useState(false);
  type InstallCommand = { args: ProjectSkillInstallArguments; draft: ProjectSkillDraft; key: string };
  const pending = useRef<InstallCommand | null>(null);
  const busy = useRef(false);
  const mounted = useRef(false);
  const owner = JSON.stringify([projectID, root, principal?.user_id, principal?.workspace_id]);
  const current = useRef({ owner, principal }); current.current = { owner, principal };
  const active = () => mounted.current && current.current.owner === owner;
  const allowedScopes = scopes.filter((value) => canInstallScope(principal, value));
  // Only an unsubmitted choice may follow changing permissions.
  const scope = pending.current?.args.scope ?? (allowedScopes.includes(selectedScope) ? selectedScope : allowedScopes[0] ?? selectedScope);
  const stale = validatedRevision !== revisionKey;

  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => { onPendingChange?.(uncertain || installing); return () => onPendingChange?.(false); }, [uncertain, installing, onPendingChange]);
  useEffect(() => {
    const controller = new AbortController();
    setChecking(true);
    setPreview(null);
    setResult(null);
    setFailure("");
    setUncertain(false); setRefreshFailed(false);
    pending.current = null;
    async function validate() {
      try {
        const draft = await api.previewProjectSkillDraft(projectID, root, controller.signal);
        validateDraftReceipt(draft, projectID, root);
        const options = await api.getSkillManagementOptions();
        if (controller.signal.aborted) return;
        setPreview(draft);
        setValidatedRevision(revisionKey);
        setScopes([...new Set(options.install_scopes.filter((value) => Object.hasOwn(scopeLabels, value)))]);
        setScope((current) => options.install_scopes.includes(current) ? current : options.install_scopes[0] ?? "project");
      } catch (error) {
        if (!controller.signal.aborted) setFailure(error instanceof Error ? error.message : "无法校验 Skill 草稿。");
      } finally { if (!controller.signal.aborted) setChecking(false); }
    }
    void validate();
    return () => controller.abort();
  }, [owner, reload]);

  const target = preview?.installations.find((item) => item.scope === scope && item.scope_ref === installScopeRef(principal, projectID, scope));
  async function refreshRegistry() {
    if (busy.current) return;
    busy.current = true; setInstalling(true); setFailure("");
    try { await onInstalled?.(); if (active()) setRefreshFailed(false); }
    catch { if (active()) setFailure("Skill 已安装，能力目录刷新失败，请重试刷新。"); }
    finally { busy.current = false; if (active()) setInstalling(false); }
  }
  async function install() {
    if (busy.current || result || checking || !allowedScopes.includes(scope) || (!pending.current && (!preview || preview.status !== "valid" || stale))) return;
    if (!pending.current) pending.current = { draft: preview!, key: createUUID(), args: {
      root_path: root, snapshot_hash: preview!.snapshot_hash, scope,
      installation_id: target?.skill_installation_id ?? "", expected_active_version_id: target?.active_version_id ?? "",
    } };
    const operation = pending.current;
    busy.current = true;
    setUncertain(true);
    setInstalling(true);
    setFailure("");
    try {
      const saved = await api.installProjectSkillDraft(projectID, operation.args, operation.key);
      if (!active() || !canInstallScope(current.current.principal, operation.args.scope)) return;
      validateInstallReceipt(saved, operation.draft, operation.args, principal);
      setResult(saved); pending.current = null; setUncertain(false);
      try { await onInstalled?.(); }
      catch { if (active()) { setRefreshFailed(true); setFailure("Skill 已安装，能力目录刷新失败，请重试刷新。"); } }
    } catch (error) {
      if (active()) {
        setFailure(error instanceof Error ? error.message : "安装失败，请重试。");
        if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") {
          pending.current = null; setUncertain(false); setPreview(null);
        }
      }
    } finally { busy.current = false; if (active()) setInstalling(false); }
  }

  return <section className="project-skill-draft" aria-label="Skill 草稿校验" aria-busy={checking || installing}>
    <div className="project-skill-actions">
      <button type="button" className="icon-button ghost" aria-label="返回文件" title="返回文件" disabled={installing || uncertain} onClick={() => { if (!busy.current && !pending.current) onBack(); }}><ArrowLeft size={16} /></button>
      <h3>Skill 草稿</h3>
      <button type="button" className="icon-button ghost" aria-label="重新校验 Skill" title="重新校验 Skill" disabled={checking || installing || uncertain} onClick={() => { if (!busy.current && !pending.current) setReload((value) => value + 1); }}><BadgeCheck size={16} /></button>
    </div>
    {checking && <p role="status">正在校验…</p>}
    {failure && <p className="form-error" role="alert">{failure}</p>}
    {stale && !result && <p role="status">工作文件已有更新，请重新校验；待确认的原安装请求仍可重试。</p>}
    {preview && <>
      <p className={preview.status === "valid" ? "project-skill-valid" : "form-error"}>{preview.status === "valid" ? "校验通过" : "校验未通过"}</p>
      {preview.diagnostics.map((item, index) => <p className="form-error" key={`${item.code}:${index}`}>{item.message || item.code}</p>)}
      {preview.manifest && <>
        <dl><dt>名称</dt><dd>{preview.manifest.name}</dd><dt>版本</dt><dd>{preview.version}</dd><dt>执行模式</dt><dd>{preview.execution_mode}</dd><dt>文件</dt><dd>{preview.files.length}</dd></dl>
        {preview.manifest.dependencies?.length ? <p>工具依赖：{preview.manifest.dependencies.map((item) => item.value).join("、")}</p> : null}
        {preview.manifest.scripts?.length ? <p>脚本：{preview.manifest.scripts.map((item) => item.path).join("、")}</p> : null}
      </>}
      {preview.status === "valid" && <>
        {!stale && <a className="project-skill-download" href={api.projectSkillDraftDownloadURL(projectID, root, preview.snapshot_hash)} download={`${preview.manifest?.name ?? "skill"}.zip`}><Download size={16} />下载 Skill ZIP</a>}
        {allowedScopes.length > 0 ? <>
          <label className="project-skill-scope">安装范围<select value={scope} disabled={installing || uncertain || stale || !!result} onChange={(event) => { if (!busy.current && !pending.current) { setScope(event.target.value as SkillInstallTarget["scope"]); setFailure(""); } }}>
            {allowedScopes.map((value) => <option key={value} value={value}>{scopeLabels[value]}</option>)}
          </select></label>
          {!result && <p>{target ? `升级已有 Skill，当前版本：${target.version}` : "新安装"}</p>}
          {!result && <button type="button" className="primary-button project-skill-install" disabled={installing || checking || !allowedScopes.includes(scope) || stale && !uncertain} onClick={() => void install()}><PackagePlus size={16} />{installing ? "正在安装…" : uncertain ? "重试原安装" : target ? "确认升级" : "确认安装"}</button>}
        </> : <p>当前角色无安装权限。</p>}
      </>}
    </>}
    {result && <p role="status">{result.installation.active_version_id !== result.receipt.skill_version_id || result.installation.status === "uninstalled" ? "该版本已保存，但已不是当前启用版本。" : result.installation.registry_status === "available" && result.installation.enabled ? "已安装，后续对话可调用。" : `已保存安装版本，当前${result.installation.enabled ? "暂不可用" : "已禁用"}。${result.installation.registry_reason_code ?? ""}`}</p>}
    {refreshFailed && <button type="button" className="secondary-button" disabled={installing} onClick={() => void refreshRegistry()}><RefreshCw size={16} />刷新能力目录</button>}
  </section>;
}

function canInstallScope(principal: Principal | null | undefined, scope: SkillInstallTarget["scope"]): boolean {
  return Boolean(principal?.kind === "user" && principal.role !== "viewer" && (scope !== "workspace" || ["admin", "owner"].includes(principal.role)));
}

function installScopeRef(principal: Principal | null | undefined, projectID: string, scope: SkillInstallTarget["scope"]): string | undefined {
  return scope === "project" ? projectID : scope === "user" ? principal?.user_id : principal?.workspace_id;
}

function validateDraftReceipt(draft: ProjectSkillDraft, projectID: string, root: string) {
  if (!draft || draft.project_id !== projectID || draft.root_path !== root || !draft.snapshot_hash || !Array.isArray(draft.files) || !Array.isArray(draft.diagnostics) || !Array.isArray(draft.installations) || draft.files.some((file) => file.project_id !== projectID || root && !file.path.startsWith(`${root}/`)) || !["valid", "invalid"].includes(draft.status) || draft.status === "valid" && (!draft.manifest?.name || !draft.manifest.content_hash || !draft.capability_id || !draft.version || !draft.execution_mode)) throw new ApiError("SKILL_DRAFT_RECEIPT_INVALID", "Skill 校验结果与当前作品或草稿不一致。", 502);
  const targets = draft.installations.map((item) => `${item.scope}:${item.scope_ref}`);
  if (new Set(targets).size !== targets.length || draft.installations.some((item) => !item.skill_installation_id || !item.active_version_id || !Object.hasOwn(scopeLabels, item.scope))) throw new ApiError("SKILL_DRAFT_RECEIPT_INVALID", "Skill 安装目标不完整或重复，请重新校验。", 502);
}

function validateInstallReceipt(saved: ProjectSkillInstallResult, draft: ProjectSkillDraft, args: ProjectSkillInstallArguments, principal: Principal | null | undefined) {
  const receipt = saved?.receipt, installation = saved?.installation;
  const version = installation?.versions?.find((item) => item.skill_version_id === receipt?.skill_version_id);
  if (!receipt?.receipt_id || receipt.project_id !== draft.project_id || !receipt.skill_version_id || !installation?.skill_installation_id || receipt.skill_installation_id !== installation.skill_installation_id || installation.workspace_id !== principal?.workspace_id || installation.scope !== args.scope || installation.scope_ref !== installScopeRef(principal, draft.project_id, args.scope) || args.installation_id && installation.skill_installation_id !== args.installation_id || installation.capability_id !== draft.capability_id || installation.skill_name !== draft.manifest?.name || !version || version.skill_installation_id !== installation.skill_installation_id || version.version !== draft.version || version.execution_mode !== draft.execution_mode || version.content_hash !== draft.manifest?.content_hash) throw new ApiError("SKILL_INSTALL_RECEIPT_INVALID", "Skill 安装回执与已确认的作品、范围或版本不一致，请重试原安装以核对结果。", 502);
}
