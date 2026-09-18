import { useEffect, useRef, useState } from "react";
import { Clock3, RefreshCw } from "lucide-react";
import { api, ApiError } from "../../api";
import type { AgentInstructionProposal, AgentToolApproval, AgentToolCall } from "../../types";
import "./agentInstructions.css";

type InstructionChangeApprovalProps = {
  call: AgentToolCall; interaction: { viewKey: string; commands: string[] };
  readOnly?: boolean;
  onResolve: (approval: AgentToolApproval, action: "approve" | "reject") => Promise<void>;
};

export function InstructionChangeApproval(props: InstructionChangeApprovalProps) {
  const { call, readOnly } = props;
  const approval = call.approval;
  const identity = JSON.stringify([call.project_id, call.agent_tool_call_id, call.arguments_hash, approval?.agent_tool_approval_id, approval?.version, approval?.status, approval?.subject_snapshot_hash, readOnly]);
  return <InstructionChangeApprovalForm key={identity} {...props} />;
}

function InstructionChangeApprovalForm({ call, interaction, readOnly, onResolve }: InstructionChangeApprovalProps) {
  const [proposal, setProposal] = useState<AgentInstructionProposal | null>(null);
  const [failure, setFailure] = useState("");
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [pendingAction, setPendingAction] = useState<"approve" | "reject" | "">("");
  const submitting = useRef(false);
  const mounted = useRef(true);
  const sequence = useRef(0);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const approval = call.approval;
  const pending = approval?.status === "pending";
  const load = async () => {
    const current = ++sequence.current;
    setLoading(true); setFailure(""); setProposal(null);
    try {
      const value = await api.getAgentInstructionProposal(call.agent_tool_call_id);
      if (current !== sequence.current) return;
      if (value.agent_tool_call_id !== call.agent_tool_call_id || value.project_id !== call.project_id || value.arguments_hash !== call.arguments_hash) throw new Error("规则提案与当前审批不一致。");
      setProposal(value);
    } catch (error) { if (current === sequence.current) setFailure(error instanceof Error ? error.message : "规则提案读取失败。"); }
    finally { if (current === sequence.current) setLoading(false); }
  };
  useEffect(() => { if (pending) void load(); else setProposal(null); return () => { sequence.current++; }; }, [call.agent_tool_call_id, call.arguments_hash, pending]);
  if (!approval) return null;
  const allowed = pending && !readOnly && !loading && !busy && interaction.viewKey === "agent_tool_approval";
  const canApprove = allowed && pendingAction !== "reject" && proposal?.can_approve && interaction.commands.includes("approve") && approval.options.includes("approve");
  const canReject = allowed && pendingAction !== "approve" && proposal?.can_reject && interaction.commands.includes("deny") && approval.options.includes("reject");
  const resolve = async (action: "approve" | "reject") => {
    if (submitting.current || (action === "approve" ? !canApprove : !canReject)) return;
    submitting.current = true;
    setBusy(true); setPendingAction(action); setFailure("");
    try { await onResolve(approval, action); }
    catch (error) {
      if (!mounted.current) return;
      setFailure(error instanceof Error ? error.message : "规则确认失败。");
      if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") { setPendingAction(""); setProposal(null); }
    }
    finally { submitting.current = false; if (mounted.current) setBusy(false); }
  };
  return <section className="approval-card agent-tool-approval-card instruction-change-approval" aria-label="规则变更确认">
    <div className="approval-icon"><Clock3 size={16} /></div>
    <div><span className="card-kicker">规则变更 · {pending ? "等待本人确认" : call.status === "completed" ? "已保存" : approval.status === "rejected" ? "已拒绝" : call.status === "failed" ? "执行失败，保存状态待核对" : call.status === "cancelled" ? "已取消" : "已授权"}</span>
      <h3>{approval.title}</h3><p>{approval.reason}</p>
      {loading && <p role="status">正在读取私有规则提案</p>}
      {proposal && <div className="instruction-change-content">
        <dl><div><dt>范围</dt><dd>{{ user: "个人", workspace: "工作区", project: "作品" }[proposal.arguments.scope]}</dd></div><div><dt>基于版本</dt><dd>v{proposal.arguments.expected_version}</dd></div><div><dt>提交后</dt><dd>{proposal.arguments.enabled ? "启用" : "停用"}</dd></div></dl>
        <details><summary>当前规则 · v{proposal.current.version}</summary><pre>{proposal.current.content || "（空）"}</pre></details>
        <strong>待保存内容</strong><pre>{proposal.arguments.content || "（清空）"}</pre>
        {!proposal.can_approve && <p className="form-error">规则版本或编辑权限已变化，当前提案不能批准。</p>}
      </div>}
      {failure && <p className="form-error" role="alert">{failure}</p>}
      {pending && <div className="approval-actions">
        <button className="icon-button" title="刷新规则提案" aria-label="刷新规则提案" disabled={busy || loading} onClick={() => void load()}><RefreshCw size={15} /></button>
        {!readOnly && proposal?.can_reject && <button className="secondary-button" disabled={!canReject} onClick={() => void resolve("reject")}>拒绝变更</button>}
        {!readOnly && proposal && <button className="primary-button" disabled={!canApprove} onClick={() => void resolve("approve")}>确认保存规则</button>}
      </div>}
    </div>
  </section>;
}
