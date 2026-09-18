import type { AgentToolExecution } from "../../types";

export function executionLabel(execution: AgentToolExecution, capabilityNames: Record<string, string> = {}): string {
  const mode = { conversation: "对话", background_task: "后台任务", stateful_workflow: "工作流" }[execution.mode] ?? "执行记录";
  const capability = execution.capability_id && (capabilityNames[execution.capability_id] || execution.capability_id);
  return capability ? `${mode} · ${capability}` : mode;
}

export function AgentToolExecutionFacts({ execution }: { execution: AgentToolExecution }) {
  const identifiers = [
    ["对话轮次", execution.agent_turn_id], ["后台任务", execution.agent_task_id],
    ["生成记录", execution.run_id], ["步骤记录", execution.step_run_id],
    ["任务记录", execution.task_item_id], ["执行尝试", execution.attempt_id],
  ].filter(([, value]) => Boolean(value));
  return <div className="execution-origin">
    <dl className="agent-tool-facts">
      {execution.step_id && <div><dt>步骤</dt><dd>{execution.step_id}</dd></div>}
      {execution.item_key && <div><dt>任务</dt><dd>{execution.item_key}</dd></div>}
      {execution.attempt_no !== undefined && <div><dt>执行次数</dt><dd>第 {execution.attempt_no} 次尝试</dd></div>}
    </dl>
    {identifiers.length > 0 && <details><summary>执行标识</summary><dl className="agent-tool-facts">{identifiers.map(([label, value]) => <div key={label}><dt>{label}</dt><dd><code>{value}</code></dd></div>)}</dl></details>}
  </div>;
}
