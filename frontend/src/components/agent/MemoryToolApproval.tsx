import { useEffect, useRef, useState } from "react";
import { RefreshCw, Wrench } from "lucide-react";
import { api, ApiError } from "../../api";
import type { AgentMemoryToolProposal, AgentToolApproval, AgentToolCall } from "../../types";
import "./agentInstructions.css";

type Props = {
  call: AgentToolCall; userID?: string; readOnly?: boolean;
  interaction: { viewKey: string; commands: string[] };
  onResolve: (approval: AgentToolApproval, action: "approve" | "reject") => Promise<void>;
};

export function MemoryToolApproval(props: Props) {
  const { call, userID, readOnly } = props, approval = call.approval;
  const identity = JSON.stringify([userID, readOnly, call.project_id, call.agent_tool_call_id, call.sdk_tool_call_id, call.arguments_hash,
    approval?.agent_tool_approval_id, approval?.version, approval?.status, approval?.subject_snapshot_hash]);
  return <MemoryToolApprovalForm key={identity} {...props} />;
}

function MemoryToolApprovalForm({ call, userID, readOnly, interaction, onResolve }: Props) {
  const [proposal, setProposal] = useState<AgentMemoryToolProposal | null>(null);
  const [failure, setFailure] = useState("");
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [pendingAction, setPendingAction] = useState<"approve" | "reject" | "">("");
  const sequence = useRef(0), mounted = useRef(true), submitting = useRef(false);
  const approval = call.approval, pending = approval?.status === "pending";
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; sequence.current++; }; }, []);
  const load = async () => {
    const seq = ++sequence.current;
    setProposal(null); setFailure(""); setLoading(true);
    try {
      if (!userID || !approval) throw new Error("当前用户身份不可用。");
      const value = await api.getAgentMemoryToolProposal(call.agent_tool_call_id);
      if (!mounted.current || seq !== sequence.current) return;
      const hasArguments = value.arguments !== undefined;
      if (value.agent_tool_call_id !== call.agent_tool_call_id || value.project_id !== call.project_id || value.user_id !== userID ||
          value.arguments_hash !== call.arguments_hash || value.approval_id !== approval.agent_tool_approval_id ||
          value.approval_version !== approval.version || value.subject_snapshot_hash !== approval.subject_snapshot_hash ||
          typeof value.generation_id !== "string" || !value.generation_id || value.generation_id.length > 256 ||
          typeof value.can_approve !== "boolean" || typeof value.can_reject !== "boolean" ||
          (hasArguments && (!value.arguments || typeof value.arguments !== "object" || Array.isArray(value.arguments))) ||
          (value.can_approve && (!hasArguments || !value.can_reject))) throw new Error("私有工具提案与当前审批不一致。");
      setProposal(value);
    } catch (error) { if (mounted.current && seq === sequence.current) setFailure(error instanceof Error ? error.message : "私有工具提案读取失败。"); }
    finally { if (mounted.current && seq === sequence.current) setLoading(false); }
  };
  useEffect(() => { if (pending) void load(); }, [pending]);
  if (!approval) return null;
  const allowed = pending && !readOnly && !loading && !busy && interaction.viewKey === "agent_tool_approval";
  const canApprove = allowed && pendingAction !== "reject" && proposal?.can_approve && interaction.commands.includes("approve") && approval.options.includes("approve");
  const canReject = allowed && pendingAction !== "approve" && proposal?.can_reject && interaction.commands.includes("deny") && approval.options.includes("reject");
  const resolve = async (action: "approve" | "reject") => {
    if (submitting.current || !(action === "approve" ? canApprove : canReject)) return;
    submitting.current = true; setBusy(true); setPendingAction(action); setFailure("");
    try { await onResolve(approval, action); }
    catch (error) {
      if (!mounted.current) return;
      setFailure(error instanceof Error ? error.message : "审批结果待核对。");
      if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 408 && error.code !== "COMMAND_IN_PROGRESS") {
        setPendingAction(""); setProposal(null);
      }
    } finally { submitting.current = false; if (mounted.current) setBusy(false); }
  };
  const status = pending ? "等待本人确认" : call.status === "completed" ? "执行完成" : approval.status === "rejected" ? "已拒绝" :
    call.status === "failed" ? "执行失败" : call.status === "cancelled" || approval.status === "cancelled" ? "已取消" : "已授权，等待执行";
  return <section className="approval-card agent-tool-approval-card instruction-change-approval memory-change-approval" aria-label="私有记忆工具确认">
    <div className="approval-icon"><Wrench size={16} /></div>
    <div><span className="card-kicker">私有记忆 · {status}</span><h3>{call.tool_name || "工具执行"}</h3>
      {loading && <p role="status">正在读取私有参数</p>}
      {proposal && <div className="instruction-change-content">
        {proposal.arguments ? <><strong>执行参数</strong><pre>{JSON.stringify(proposal.arguments, null, 2)}</pre></> : <p className="form-error">执行参数已失效或不可用，不能批准。</p>}
      </div>}
      {failure && <p className="form-error" role="alert">{failure}</p>}
      {pending && <div className="approval-actions">
        <button className="icon-button" title="刷新私有工具提案" aria-label="刷新私有工具提案" disabled={loading || busy} onClick={() => void load()}><RefreshCw size={15} /></button>
        {!readOnly && proposal?.can_reject && <button className="secondary-button" disabled={!canReject} onClick={() => void resolve("reject")}>拒绝执行</button>}
        {!readOnly && proposal && <button className="primary-button" disabled={!canApprove} onClick={() => void resolve("approve")}>允许执行</button>}
      </div>}
    </div>
  </section>;
}
