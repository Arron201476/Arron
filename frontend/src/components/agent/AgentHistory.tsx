import { useMemo, useState } from "react";
import { ChevronDown, CircleAlert, Clock3, FileText, History, TextQuote } from "lucide-react";
import { stepLabels } from "../../presentation";
import type { Asset, Message, ProjectActivity } from "../../types";

export function TimelineMessage({ message, assets = [], capabilityNames = {} }: { message: Message; assets?: Asset[]; capabilityNames?: Record<string, string> }) {
  const [expanded, setExpanded] = useState(false);
  const long = message.content.length > 360;
  const selection = message.message_context?.selection_snapshot?.selection;
  const routing = message.message_context?.routing_context;
  const scopeLabel = routing?.scope === "run" || routing?.scope === "artifact"
    ? capabilityNames[routing.capability_id ?? ""]
    : routing?.scope === "capability"
      ? capabilityNames[routing.capability_id ?? ""]
      : "";
  const attachments = (message.message_context?.attachment_refs ?? []).filter((reference) => !reference.hidden).map((reference) => {
    const asset = assets.find((item) => item.asset_id === reference.asset_id);
    const embeddedName = (reference as typeof reference & { display_name?: string }).display_name;
    return {
      key: reference.asset_snapshot_id,
      name: asset?.display_name || asset?.original_filename || embeddedName || "已上传文件",
      kind: asset?.kind || "file",
    };
  });
  const pendingLabel = ({ sending: "发送中", unconfirmed: "发送结果未确认", queued: "等待处理", running: "处理中", waiting_approval: "等待工具授权", pausing: "暂停中", paused: "已暂停", committing: "保存中", cancelling: "停止中", failed: "处理失败", cancelled: "已停止" } as Record<string, string>)[message.delivery_status ?? ""];
  const active = ["sending", "queued", "running", "pausing", "committing", "cancelling"].includes(message.delivery_status ?? "");
  const waitingApproval = message.delivery_status === "waiting_approval" || message.delivery_status === "paused";
  const activeLabel = message.delivery_status === "sending" ? "正在发送" : pendingLabel;
  const failureMessage = message.delivery_error?.trim() || "处理失败，内容已保留";
  return <div className={`timeline-message ${message.role} ${message.delivery_status ?? ""}`}><span className="message-meta"><strong>{message.role === "user" ? "你" : "Agent"}</strong><span>{scopeLabel}</span><time>{pendingLabel || formatActivityTime(message.created_at)}</time></span>{selection && <div className="message-reference"><TextQuote size={13} /><span><strong>{[selection.display.artifact_label, selection.display.location_label].filter(Boolean).join(" · ")}</strong><small>{selection.display.selected_text_summary}</small></span></div>}{attachments.length > 0 && <div className="message-attachments" aria-label="本条消息的附件">{attachments.map((attachment) => <span key={attachment.key} title={attachment.name}><FileText size={13} /><span><strong>{attachment.name}</strong><small>{attachment.kind === "video" ? "视频" : attachment.kind === "image" ? "图片" : "文件"}</small></span></span>)}</div>}<p className={long && !expanded ? "collapsed" : ""}>{message.content}</p>{active && <span className="message-delivery"><span className="mini-spinner" />{activeLabel}</span>}{waitingApproval && <span className="message-delivery"><Clock3 size={13} />{pendingLabel}</span>}{message.delivery_status === "failed" && <span className="message-delivery failed" role="alert"><CircleAlert size={13} /><span>{failureMessage}</span></span>}{message.delivery_status === "cancelled" && <span className="message-delivery">本轮已停止，内容已保留</span>}{long && <button className="message-expand" onClick={() => setExpanded(!expanded)}>{expanded ? "收起" : "展开全部"}<ChevronDown size={13} /></button>}</div>;
}

export function ProcessHistory({ activities, activeRunID, capabilityNames = {} }: { activities: ProjectActivity[]; activeRunID: string | null; capabilityNames?: Record<string, string> }) {
  const groups = useMemo(() => {
    const grouped = new Map<string, ProjectActivity[]>();
    for (const activity of activities) {
      const key = activity.run_id ?? "project";
      grouped.set(key, [...(grouped.get(key) ?? []), activity]);
    }
    return [...grouped.entries()].map(([runID, items]) => ({ runID, items })).sort((left, right) => right.items.at(-1)!.occurred_at.localeCompare(left.items.at(-1)!.occurred_at));
  }, [activities]);

  return <section className="process-history"><header><div><History size={15} /><strong>过程记录</strong></div><span>{groups.filter((group) => group.runID !== "project").length} 次生成</span></header>{groups.map((group) => {
    const capabilityID = group.items.find((item) => item.capability_id)?.capability_id;
    const completedSteps = new Set(group.items.filter((item) => item.event_type === "step.completed" && item.step_id).map((item) => item.step_id)).size;
    const failedSteps = group.items.filter((item) => item.event_type === "step.failed").length;
    const latest = group.items.at(-1)!;
    const runState = [...group.items].reverse().find((item) => item.event_type.startsWith("run.")) ?? latest;
    const active = group.runID === activeRunID;
    const completedRun = runState.event_type === "run.completed";
    const summaryItems = summarizeActivities(group.items);
    return <details key={group.runID} className={`process-run ${active ? "current" : ""}`} open={active && !completedRun}><summary><span><strong>{group.runID === "project" ? "作品状态" : capabilityNames[capabilityID ?? ""] ?? "内容生成"}</strong><small>{completedSteps ? `${completedSteps} 个步骤已完成` : activityLabel(latest)}{failedSteps && !completedRun ? ` · ${failedSteps} 个失败` : ""}</small></span><span className={`process-state ${activityTone(runState.event_type)}`}>{activityLabel(runState)}</span><ChevronDown size={14} /></summary><div className="process-events">{summaryItems.map((item) => <div key={item.event_id} className={activityTone(item.event_type)}><span className="process-dot" /><div><strong>{item.step_id ? stepLabels[item.step_id] ?? "处理生成步骤" : activityLabel(item)}</strong><small>{activityLabel(item)} · {formatActivityTime(item.occurred_at)}</small></div></div>)}</div></details>;
  })}</section>;
}

export function summarizeActivities(items: ProjectActivity[]) {
  const latestByStage = new Map<string, ProjectActivity>();
  for (const item of items) {
    if (item.event_type.startsWith("run.") || item.event_type === "step.started") continue;
    const key = item.step_id ? `step:${item.step_id}` : `event:${item.event_type.replace(/\.(started|passed|action_required|override_confirmed|created|changed)$/, "")}`;
    latestByStage.set(key, item);
  }
  return [...latestByStage.values()].sort((left, right) => left.occurred_at.localeCompare(right.occurred_at));
}

function formatActivityTime(value: string) {
  const date = new Date(value);
  const now = new Date();
  if (date.toDateString() === now.toDateString()) return `今天 ${date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" })}`;
  return date.toLocaleDateString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

function activityLabel(activity: ProjectActivity) {
  const labels: Record<string, string> = {
    "run.started": "已开始", "run.paused": "已暂停", "run.resumed": "已恢复", "run.failed": "运行失败", "run.completed": "已完成", "run.cancelled": "已结束",
    "step.started": "处理中", "step.completed": "已完成", "step.failed": "处理失败", "approval.requested": "等待确认", "approval.resolved": "已确认",
    "workflow.failure_transition_prepared": "失败后续步骤待继续", "workflow.failure_transition_blocked": "失败后续步骤无法继续",
    "quality_review.started": "开始质量审核", "quality_review.passed": "质量审核通过", "quality_review.action_required": "审核需要处理", "quality_review.override_confirmed": "已确认保留风险",
    "script_candidate.created": "已生成候选稿", "final_selection.changed": "最终稿已更新",
  };
  return labels[activity.event_type] ?? "状态已更新";
}

function activityTone(eventType: string) {
  if (eventType.includes("failed") || eventType === "quality_review.action_required" || eventType === "workflow.failure_transition_blocked") return "failed";
  if (eventType.includes("paused") || eventType === "approval.requested" || eventType === "workflow.failure_transition_prepared") return "waiting";
  if (eventType.includes("completed") || eventType.includes("passed") || eventType.includes("confirmed") || eventType.includes("selection.changed") || eventType.includes("candidate.created")) return "completed";
  return "running";
}
