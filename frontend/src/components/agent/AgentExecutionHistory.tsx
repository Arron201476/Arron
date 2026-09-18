import { GitBranch, Wrench } from "lucide-react";
import type { AgentToolCall, AgentToolExecution, AgentTurn, Asset } from "../../types";
import { AssetFileActions } from "../files/ProjectMaterialPicker";
import { citationLink, toolOutputAssets } from "../files/projectMaterials";
import { AgentToolExecutionFacts, executionLabel } from "./AgentToolExecutionFacts";
import { AgentTurnInputs } from "./AgentTaskInputs";
import { AgentSubtaskResult } from "./AgentSubtaskResult";
import { AgentToolOutcomeReview } from "./AgentToolOutcomeReview";

const statuses: Record<string, string> = {
  accepted: "排队中", running: "执行中", waiting_approval: "等待授权", cancel_requested: "停止中",
  pausing: "暂停中", paused: "已暂停",
  committing: "保存中", committed: "已完成", completed: "已完成", failed: "失败", cancelled: "已停止",
  pending_approval: "等待授权", approved: "已授权", rejected: "已拒绝",
};

const failureStages: Record<string, string> = {
  session_prepare: "会话准备", session_compaction: "原生上下文压缩",
};

function ToolRecord({ call, assets, children = [], onUseAsset }: { call: AgentToolCall; assets: Asset[]; children?: AgentToolCall[]; onUseAsset?: (asset: Asset) => void }) {
  const outputs = toolOutputAssets(call, assets);
  const subtask = call.tool_id === "runtime:delegate_subtask";
  const args = call.arguments_summary;
  const title = args && typeof args === "object" && "title" in args && typeof args.title === "string" ? args.title : "子任务";
  const Icon = subtask ? GitBranch : Wrench;
  return <div className={`execution-tool ${call.status}`}>
    <Icon size={13} /><div><strong>{subtask ? title : call.tool_name}</strong><small>{subtask ? "子任务" : call.tool_kind} · {statuses[call.status] ?? call.status}</small>{call.error_code && <p className="form-error">{call.error_code}{call.error_message ? ` · ${call.error_message}` : ""}</p>}
      {subtask && call.status === "completed" && <AgentSubtaskResult key={`${call.project_id}:${call.agent_tool_call_id}`} callID={call.agent_tool_call_id} projectID={call.project_id} />}
      {call.program_call_id && <small title={call.program_call_id}>程序化调用</small>}
      {call.tool_kind === "mcp" && call.access_mode !== "read" && call.started_at && (call.status === "failed" || call.status === "cancelled") && <AgentToolOutcomeReview key={`${call.project_id}:${call.agent_tool_call_id}`} callID={call.agent_tool_call_id} projectID={call.project_id} />}
      {subtask && children.length > 0 && <details className="agent-subtask-reads"><summary>{children.length} 次子任务读取</summary>{children.map((child) => <ToolRecord key={child.agent_tool_call_id} call={child} assets={assets} onUseAsset={onUseAsset} />)}</details>}
      {outputs.map((asset) => <div className="execution-output-file" key={asset.asset_id}><span>{asset.original_filename}{asset.status === "deleted" ? "（已删除）" : asset.status === "expired" ? "（已过期）" : ""}</span><AssetFileActions asset={asset} onUse={onUseAsset} /></div>)}
      {call.tool_kind === "hosted" && Boolean(call.citations?.length) && <ul className="execution-citations" aria-label="工具来源引用">{call.citations?.map((citation, index) => { const url = citation.type === "url_citation" ? citationLink(citation.url) : null; return <li key={index}>{url ? <a href={url} target="_blank" rel="noopener noreferrer">{citation.title || url}</a> : citation.type === "file_citation" ? <span title={citation.file_id}>检索文件：{citation.filename || citation.file_id}</span> : <span>引用地址不可用</span>}</li>; })}</ul>}
      {Boolean(call.omitted_citations) && <small>{call.omitted_citations} 条引用未展示</small>}
    </div>
  </div>;
}

function ToolRecords({ calls, assets, onUseAsset }: { calls: AgentToolCall[]; assets: Asset[]; onUseAsset?: (asset: Asset) => void }) {
  const ordered = [...calls].sort((left, right) => Date.parse(left.requested_at) - Date.parse(right.requested_at) || 0);
  const parents = new Set(ordered.filter((call) => call.tool_id === "runtime:delegate_subtask").map((call) => call.agent_tool_call_id));
  return <>{ordered.filter((call) => !call.parent_tool_call_id || !parents.has(call.parent_tool_call_id)).map((call) => <ToolRecord key={call.agent_tool_call_id} call={call} assets={assets} onUseAsset={onUseAsset} children={ordered.filter((child) => child.parent_tool_call_id === call.agent_tool_call_id)} />)}</>;
}

function conversationTurnID(call: AgentToolCall): string | null | undefined {
  return call.execution ? call.execution.mode === "conversation" ? call.execution.agent_turn_id : undefined : call.agent_turn_id;
}

export function AgentExecutionHistory({ turns, calls, assets = [], capabilityNames = {}, onUseAsset }: { turns: AgentTurn[]; calls: AgentToolCall[]; assets?: Asset[]; capabilityNames?: Record<string, string>; onUseAsset?: (asset: Asset) => void }) {
  if (!turns.length && !calls.length) return null;
  const detached = calls.filter((call) => !turns.some((turn) => turn.agent_turn_id === conversationTurnID(call)));
  const groups = new Map<string, { execution?: AgentToolExecution | null; calls: AgentToolCall[] }>();
  for (const call of detached) {
    const execution = call.execution;
    const id = execution?.mode === "conversation" ? execution.agent_turn_id : execution?.attempt_id;
    const key = execution && id ? `${execution.mode}:${id}` : "unbound";
    const group = groups.get(key) ?? { execution: id ? execution : null, calls: [] };
    group.calls.push(call);
    groups.set(key, group);
  }
  return <section className="execution-history" aria-label="Agent 执行记录"><h3>Agent 执行记录</h3>
    {[...turns].reverse().map((turn) => {
      const tools = calls.filter((call) => conversationTurnID(call) === turn.agent_turn_id)
        .sort((left, right) => Date.parse(left.requested_at) - Date.parse(right.requested_at) || 0);
      const observation = turn.observation;
      return <details key={turn.agent_turn_id} className="execution-turn"><summary><span><strong>{turn.request.content || "Agent 请求"}</strong><small>{statuses[turn.status] ?? turn.status} · {tools.length} 次工具调用</small></span></summary>
        <dl className="skill-facts">
          <div><dt>开始时间</dt><dd>{new Date(turn.created_at).toLocaleString("zh-CN")}</dd></div>
          {observation?.model_id && <div><dt>模型</dt><dd>{observation.model_id}</dd></div>}
          {observation?.provider_id && <div><dt>接口</dt><dd>{observation.provider_id}</dd></div>}
          {observation?.usage?.total_tokens !== undefined && <div><dt>Token 用量</dt><dd>{observation.usage.total_tokens.toLocaleString("zh-CN")}</dd></div>}
          {observation?.failure_stage && <div><dt>失败阶段</dt><dd>{failureStages[observation.failure_stage] ?? observation.failure_stage}</dd></div>}
        </dl>
        {turn.error_code && <p className="form-error">{turn.error_code}{turn.error_message ? ` · ${turn.error_message}` : ""}</p>}
        <AgentTurnInputs turn={turn} />
        <ToolRecords calls={tools} assets={assets} onUseAsset={onUseAsset} />
      </details>;
    })}
    {[...groups].map(([key, group]) => <details key={key} className="execution-turn">
      <summary><span><strong>{group.execution ? executionLabel(group.execution, capabilityNames) : "未关联的工具调用"}</strong><small>{group.execution?.step_id ? `${group.execution.step_id} · ` : ""}{group.execution?.attempt_no ? `第 ${group.execution.attempt_no} 次尝试 · ` : ""}{group.calls.length} 次工具调用</small></span></summary>
      {group.execution && <AgentToolExecutionFacts execution={group.execution} />}
      <ToolRecords calls={group.calls} assets={assets} onUseAsset={onUseAsset} />
    </details>)}
  </section>;
}
