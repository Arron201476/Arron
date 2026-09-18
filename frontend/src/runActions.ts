import type { AvailableAction, RunSnapshot } from "./types";

export function runActionTargetMatches(runID: string, action: AvailableAction): boolean {
  if (!runID || !action.enabled || !action.target_id) return false;
  if (["pause_run", "resume_run", "cancel_run"].includes(action.action_id)) return action.target_type === "run" && action.target_id === runID;
  return ["retry_failed_step", "continue_with_partial_results"].includes(action.action_id) && action.target_type === "step_run";
}

export function runActionMatchesSnapshot(snapshot: RunSnapshot, action: AvailableAction): boolean {
  if (!runActionTargetMatches(snapshot.run.run_id, action)) return false;
  if (action.target_type === "run") return true;
  return action.target_id === snapshot.run.current_step_run_id && Boolean(snapshot.steps?.some((step) => step.step_run_id === action.target_id && step.run_id === snapshot.run.run_id));
}

export function runActionIdentity(action: AvailableAction): string {
  return JSON.stringify([action.action_id, action.target_type, action.target_id]);
}
