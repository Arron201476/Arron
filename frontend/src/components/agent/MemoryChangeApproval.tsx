import { useEffect, useRef, useState } from "react";
import { Brain, RefreshCw } from "lucide-react";
import { api, ApiError } from "../../api";
import type { AgentMemoryProposal, AgentToolApproval, AgentToolCall } from "../../types";
import "./agentInstructions.css";

type Props = {
  call: AgentToolCall; userID?: string; readOnly?: boolean;
  interaction: { viewKey: string; commands: string[] };
  onResolve: (approval: AgentToolApproval, action: "approve" | "reject") => Promise<void>;
};

export function MemoryChangeApproval(props: Props) {
  const { call, userID, readOnly } = props;
  const approval = call.approval;
  const identity = JSON.stringify([userID, readOnly, call.project_id, call.agent_tool_call_id, call.arguments_hash, approval?.agent_tool_approval_id, approval?.version, approval?.status, approval?.subject_snapshot_hash]);
  return <MemoryChangeApprovalForm key={identity} {...props} />;
}

function MemoryChangeApprovalForm({ call, userID, readOnly, interaction, onResolve }: Props) {
  const [proposal, setProposal] = useState<AgentMemoryProposal | null>(null);
  const [failure, setFailure] = useState("");
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [pendingAction, setPendingAction] = useState<"approve" | "reject" | "">("");
  const sequence = useRef(0);
  const mounted = useRef(true);
  const submitting = useRef(false);
  const approval = call.approval;
  const pending = approval?.status === "pending";
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; sequence.current++; }; }, []);
  const load = async () => {
    const seq = ++sequence.current;
    setProposal(null); setFailure(""); setLoading(true);
    try {
      if (!userID || !approval) throw new Error("当前用户身份不可用。");
      const value = await api.getAgentMemoryProposal(call.agent_tool_call_id);
      if (!mounted.current || seq !== sequence.current) return;
      if (value.agent_tool_call_id !== call.agent_tool_call_id || value.project_id !== call.project_id || value.user_id !== userID ||
          value.arguments_hash !== call.arguments_hash || value.approval_id !== approval.agent_tool_approval_id || value.approval_version !== approval.version ||
          value.subject_snapshot_hash !== approval.subject_snapshot_hash || value.current?.project_id !== call.project_id || value.current?.user_id !== userID ||
          typeof value.available !== "boolean" || typeof value.can_approve !== "boolean" || typeof value.can_reject !== "boolean" ||
          !validFiles(value.files) || !validFiles(value.current.files) || !Number.isSafeInteger(value.expected_version) || value.expected_version < 0 ||
          !Number.isSafeInteger(value.current.version) || value.current.version < 0 ||
          (value.available && (value.current.version !== value.expected_version || !value.files["memory_summary.md"]?.trim())) ||
          (!value.available && (value.can_approve || Object.keys(value.files).length > 0))) throw new Error("私有记忆提案与当前审批不一致。");
      setProposal(value);
    } catch (error) { if (mounted.current && seq === sequence.current) setFailure(error instanceof Error ? error.message : "私有记忆读取失败。"); }
    finally { if (mounted.current && seq === sequence.current) setLoading(false); }
  };
  useEffect(() => { if (pending) void load(); }, [pending]);
  if (!approval) return null;
  const allowed = pending && !readOnly && !loading && !busy && interaction.viewKey === "agent_tool_approval";
  const canApprove = allowed && pendingAction !== "reject" && proposal?.available && proposal.can_approve && interaction.commands.includes("approve") && approval.options.includes("approve");
  const canReject = allowed && pendingAction !== "approve" && proposal?.can_reject && interaction.commands.includes("deny") && approval.options.includes("reject");
  const resolve = async (action: "approve" | "reject") => {
    if (submitting.current || !(action === "approve" ? canApprove : canReject)) return;
    submitting.current = true; setBusy(true); setPendingAction(action); setFailure("");
    try { await onResolve(approval, action); }
    catch (error) {
      if (!mounted.current) return;
      setFailure(error instanceof Error ? error.message : "记忆审批结果待核对。");
      if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") {
        setPendingAction(""); setProposal(null);
      }
    } finally { submitting.current = false; if (mounted.current) setBusy(false); }
  };
  const status = pending ? "等待本人确认" : call.status === "completed" ? "已保存" : approval.status === "rejected" ? "已拒绝" : call.status === "failed" ? "保存状态待核对" : call.status === "cancelled" ? "已取消" : "已授权，等待保存";
  const names = proposal?.available ? [...new Set([...Object.keys(proposal.current.files), ...Object.keys(proposal.files)])].sort() : [];
  return <section className="approval-card agent-tool-approval-card instruction-change-approval memory-change-approval" aria-label="私有记忆保存确认">
    <div className="approval-icon"><Brain size={16} /></div>
    <div><span className="card-kicker">私有记忆 · {status}</span><h3>确认保存作品记忆</h3>
      {loading && <p role="status">正在读取私有候选内容</p>}
      {proposal && <div className="instruction-change-content">
        <dl><div><dt>当前版本</dt><dd>v{proposal.current.version}</dd></div><div><dt>基于版本</dt><dd>v{proposal.expected_version}</dd></div><div><dt>保存后状态</dt><dd>启用</dd></div></dl>
        {!proposal.available && <p className="form-error">候选内容已失效或不可用，不能批准。</p>}
        {names.map(name => {
          const had = Object.hasOwn(proposal.current.files, name), has = Object.hasOwn(proposal.files, name);
          const state = !has ? "删除" : !had ? "新增" : proposal.current.files[name] === proposal.files[name] ? "不变" : "替换";
          return <details key={name}><summary>{name} · {state}</summary>
            <strong>当前内容</strong><pre>{had ? proposal.current.files[name] || "（空文件）" : "（不存在）"}</pre>
            <strong>保存后内容</strong><pre>{has ? proposal.files[name] || "（空文件）" : "（删除）"}</pre>
          </details>;
        })}
      </div>}
      {failure && <p className="form-error" role="alert">{failure}</p>}
      {pending && <div className="approval-actions">
        <button className="icon-button" title="刷新记忆提案" aria-label="刷新记忆提案" disabled={loading || busy} onClick={() => void load()}><RefreshCw size={15} /></button>
        {!readOnly && proposal?.can_reject && <button className="secondary-button" disabled={!canReject} onClick={() => void resolve("reject")}>拒绝保存</button>}
        {!readOnly && proposal && <button className="primary-button" disabled={!canApprove} onClick={() => void resolve("approve")}>确认保存记忆</button>}
      </div>}
    </div>
  </section>;
}

function validFiles(value: unknown): value is Record<string, string> {
  return !!value && typeof value === "object" && !Array.isArray(value) && Object.values(value).every(body => typeof body === "string");
}
