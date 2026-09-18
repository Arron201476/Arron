import type { Project } from "../../types";
import "./projectGoal.css";

export function ProjectGoalSummary({ project, goal }: { project: Project; goal: unknown }) {
  if (!goal || typeof goal !== "object" || Array.isArray(goal)) return null;
  const value = goal as Record<string, unknown>;
  if (value.project_id !== project.project_id || value.conversation_id !== project.primary_conversation_id ||
      value.status !== "active" || typeof value.goal_id !== "string" || !value.goal_id ||
      typeof value.title !== "string" || !value.title.trim() ||
      !Number.isSafeInteger(value.version) || Number(value.version) < 1 ||
      !Array.isArray(value.success_criteria) || !value.success_criteria.every((item) => typeof item === "string")) return null;
  return <details className="project-goal-summary" aria-label="当前项目目标">
    <summary><strong>当前目标</strong><span>{value.title}</span></summary>
    <div><small>进行中 · v{String(value.version)}</small>
      {value.success_criteria.length > 0 && <><h3>完成条件</h3><ul>{value.success_criteria.map((criterion, index) => <li key={index}>{criterion}</li>)}</ul></>}
    </div>
  </details>;
}
