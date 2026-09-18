// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ApprovalCard } from "./App";
import { api, ApiError } from "./api";
import type { Approval, ArtifactVersion, QualityReview, RevisionRequest } from "./types";
import { resolveApprovalViewKey } from "./workspaceProjection";

const approval = (overrides: Partial<Approval> = {}): Approval => ({
  approval_request_id: "approval", run_id: "run", scope: "transition", status: "pending", version: 1,
  title: "Choose a direction", reason: "One choice", options: ["select_single_option", "request_ai_revision"],
  subject_kind: "artifact_version", subject_ref_id: "av1", subject_version: 1, subject_snapshot_hash: "hash1",
  requested_at: "2026-09-09T00:00:00Z", ...overrides,
});
function version(id = "av1", number = 1, count = 5): ArtifactVersion {
  return { artifact_version_id: id, artifact_id: "artifact", version: number, status: "pending_approval", creation_reason: "generated", created_at: "2026-09-09T00:00:00Z",
    payload: { core_settings: {}, selection_instructions: "Select one", options: Array.from({ length: count }, (_, index) => ({ option_id: `option_${index + 1}`, title: `${id} Choice ${index + 1}`, genre_tag: "Genre", summary: "Summary", outline: "Outline" })) },
  };
}
const props = () => ({ approval: approval(), viewKey: "single_option" as const, blockingRevision: null, onResolve: vi.fn(async () => {}) });
const confirm = () => screen.getByRole("button", { name: /确认方向并续写|确认选择并继续/ });
beforeEach(() => { vi.spyOn(api, "getArtifactVersion").mockResolvedValue(version()); });
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("renders a workflow continuation as a bound confirmation without an expansion strategy", async () => {
  const subject = approval({ scope: "workflow_transition", subject_kind: "transition", subject_ref_id: "decision-1", title: "Continue workflow", options: ["approve"] });
  const viewKey = resolveApprovalViewKey([
    { registry_key: "workflow_transition", view_key: "action_list", match: { scope: "workflow_transition", subject_kind: "transition" } },
    { registry_key: "volume_fit", view_key: "volume_fit", match: { subject_kind: "transition" } },
  ], subject);
  const onResolve = vi.fn(async () => {});
  render(<ApprovalCard approval={subject} viewKey={viewKey} blockingRevision={null} onResolve={onResolve} />);
  expect(screen.queryByRole("textbox")).toBeNull();
  expect(api.getArtifactVersion).not.toHaveBeenCalled();
  expect(onResolve).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "确认并继续" }));
  await waitFor(() => expect(onResolve).toHaveBeenCalledExactlyOnceWith(subject, "approve", "", undefined));
});

it.each(["batch", "artifact"])("confirms a generic %s version set without script-only controls", async (scope) => {
  const subject = approval({ scope, subject_kind: "artifact_version_set", subject_ref_id: "step-reviews", subject_version: 3, subject_snapshot_hash: "three-report-versions", options: ["approve"] });
  const viewKey = resolveApprovalViewKey([
    { registry_key: "generic", view_key: "action_list", match: {} },
  ], subject);
  const onResolve = vi.fn(async () => {});
  render(<ApprovalCard approval={subject} viewKey={viewKey} blockingRevision={null} onResolve={onResolve} />);
  expect(api.getArtifactVersion).not.toHaveBeenCalled();
  expect(screen.queryByRole("textbox")).toBeNull();
  expect(screen.queryByRole("checkbox")).toBeNull();
  expect(onResolve).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "确认并继续" }));
  await waitFor(() => expect(onResolve).toHaveBeenCalledExactlyOnceWith(subject, "approve", "", undefined));
});

it("requires an explicit single selection and submits the bound artifact version", async () => {
  const input = props();
  render(<ApprovalCard {...input} />);
  const radios = await screen.findAllByRole("radio");
  expect(radios).toHaveLength(5);
  expect(radios.every((radio) => !(radio as HTMLInputElement).checked)).toBe(true);
  expect(confirm()).toHaveProperty("disabled", true);
  fireEvent.click(radios[2]);
  fireEvent.click(confirm());
  await waitFor(() => expect(input.onResolve).toHaveBeenCalledExactlyOnceWith(input.approval, "select_single_option", "", { options_artifact_version_id: "av1", selected_option_id: "option_3" }));
});

it("discards old choices immediately when the approval subject is replaced", async () => {
  const input = props();
  const view = render(<ApprovalCard {...input} />);
  fireEvent.click((await screen.findAllByRole("radio"))[2]);
  let complete!: (value: ArtifactVersion) => void;
  vi.mocked(api.getArtifactVersion).mockImplementationOnce(() => new Promise((resolve) => { complete = resolve; }));
  view.rerender(<ApprovalCard {...input} approval={approval({ version: 2, subject_version: 2, subject_ref_id: "av2", subject_snapshot_hash: "hash2" })} />);
  expect(screen.queryAllByRole("radio")).toHaveLength(0);
  await act(async () => { complete(version("av2", 2)); });
  expect(confirm()).toHaveProperty("disabled", true);
  expect(screen.queryByText(/av1 Choice/)).toBeNull();
});

it("retries a failed subject read without a mutation or a permanent loading message", async () => {
  vi.mocked(api.getArtifactVersion).mockRejectedValueOnce(new ApiError("UNAVAILABLE", "Read unavailable", 503));
  const input = props();
  render(<ApprovalCard {...input} />);
  await screen.findByText("Read unavailable");
  expect(screen.queryByText(/正在读取.*方向|正在读取.*候选/)).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "重试读取确认内容" }));
  expect(await screen.findAllByRole("radio")).toHaveLength(5);
  expect(api.getArtifactVersion).toHaveBeenCalledTimes(2);
  expect(input.onResolve).not.toHaveBeenCalled();
});

it.each(["identity", "version", "duplicate", "missing", "invalid_title"])("rejects a subject response with broken %s", async (change) => {
  const response = version();
  const payload = response.payload as { options: { option_id: string; title: unknown }[] };
  if (change === "identity") response.artifact_version_id = "another-version";
  if (change === "version") response.version = 99;
  if (change === "duplicate") payload.options[1].option_id = payload.options[0].option_id;
  if (change === "missing") response.payload = null;
  if (change === "invalid_title") payload.options[1].title = { unexpected: true };
  vi.mocked(api.getArtifactVersion).mockResolvedValue(response);
  render(<ApprovalCard {...props()} />);
  await screen.findByRole("button", { name: "重试读取确认内容" });
  expect(screen.queryAllByRole("radio")).toHaveLength(0);
});

it("supports a plugin's three-option single-choice payload without imposing the continuation count", async () => {
  vi.mocked(api.getArtifactVersion).mockResolvedValue(version("av1", 1, 3));
  render(<ApprovalCard {...props()} />);
  expect(await screen.findAllByRole("radio")).toHaveLength(3);
});

it("locks the selected direction while its confirmation is in flight", async () => {
  const input = props();
  let complete!: () => void;
  input.onResolve.mockImplementation(() => new Promise((resolve) => { complete = resolve; }));
  render(<ApprovalCard {...input} />);
  const radios = await screen.findAllByRole("radio");
  fireEvent.click(radios[1]); fireEvent.click(confirm());
  expect(radios.every((radio) => (radio as HTMLInputElement).disabled)).toBe(true);
  await act(async () => { complete(); });
});

it("does not offer a single-choice command absent from the approval options", async () => {
  render(<ApprovalCard {...props()} approval={approval({ options: ["request_ai_revision"] })} />);
  await screen.findAllByRole("radio");
  expect(screen.queryByRole("button", { name: /确认方向并续写|确认选择并继续/ })).toBeNull();
});

it.each(["approved", "expired", "cancelled"])("makes a %s approval read-only", async (status) => {
  const input = props();
  render(<ApprovalCard {...input} approval={approval({ status })} />);
  await act(async () => {});
  expect(screen.queryAllByRole("radio")).toHaveLength(0);
  expect(input.onResolve).not.toHaveBeenCalled();
});

it("blocks a revision submit when a competing revision arrives after editing began", async () => {
  const input = props();
  const view = render(<ApprovalCard {...input} />);
  await screen.findAllByRole("radio");
  fireEvent.click(screen.getByRole("button", { name: /让 Agent 修改方向|让 Agent 修改候选/ }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Keep the premise" } });
  view.rerender(<ApprovalCard {...input} blockingRevision={{ instruction: "Other change", status: "running" } as RevisionRequest} />);
  expect(screen.getByRole("button", { name: "提交修改要求" })).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "提交修改要求" }));
  expect(input.onResolve).not.toHaveBeenCalled();
});

it("ignores an old subject result after a different approval has been mounted", async () => {
  let complete!: (value: ArtifactVersion) => void;
  vi.mocked(api.getArtifactVersion).mockImplementationOnce(() => new Promise((resolve) => { complete = resolve; }));
  const view = render(<ApprovalCard {...props()} />);
  vi.mocked(api.getArtifactVersion).mockResolvedValue(version("av2", 2));
  view.rerender(<ApprovalCard {...props()} approval={approval({ approval_request_id: "next", subject_ref_id: "av2", subject_version: 2 })} />);
  await screen.findAllByRole("radio");
  await act(async () => { complete(version()); });
  expect(screen.queryByText(/av1 Choice/)).toBeNull();
  expect(screen.getByText(/av2 Choice 1/)).toBeTruthy();
});

it("preserves the selected candidate after a rejected submission", async () => {
  const input = props();
  input.onResolve.mockRejectedValue(new ApiError("UNAVAILABLE", "Try again", 503));
  render(<ApprovalCard {...input} />);
  const radios = await screen.findAllByRole("radio");
  fireEvent.click(radios[3]); fireEvent.click(confirm());
  await screen.findByText("Try again");
  expect(radios[3]).toHaveProperty("checked", true);
  expect(confirm()).toHaveProperty("disabled", false);
});

it("removes an old revision draft and late failure when the approval version changes", async () => {
  const input = props();
  let reject!: (value: unknown) => void;
  input.onResolve.mockImplementation(() => new Promise((_, fail) => { reject = fail; }));
  const view = render(<ApprovalCard {...input} />);
  await screen.findAllByRole("radio");
  fireEvent.click(screen.getByRole("button", { name: "让 Agent 修改候选" }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Old revision" } });
  fireEvent.click(screen.getByRole("button", { name: "提交修改要求" }));
  view.rerender(<ApprovalCard {...input} approval={approval({ version: 2, subject_snapshot_hash: "new" })} />);
  await screen.findAllByRole("radio");
  await act(async () => { reject(new ApiError("OLD", "Old failure", 409)); });
  expect(screen.queryByRole("textbox")).toBeNull();
  expect(screen.queryByText("Old failure")).toBeNull();
});

it("does not fetch or mutate a read-only approval", async () => {
  const input = props();
  render(<ApprovalCard {...input} readOnly />);
  await act(async () => {});
  expect(api.getArtifactVersion).not.toHaveBeenCalled();
  expect(screen.queryAllByRole("radio")).toHaveLength(0);
});

it("preserves adaptation's multi-choice and structured creation configuration", async () => {
  const input = props();
  vi.mocked(api.getArtifactVersion).mockResolvedValue({ ...version(), payload: { selection_instructions: "Combine choices", options: ["one", "two"].map((id) => ({ option_id: id, title: id, one_sentence_strategy: "Strategy", risks: [] })) } });
  const request = approval({ options: ["select_adaptation_strategy", "request_ai_revision"] });
  render(<ApprovalCard {...input} approval={request} viewKey="adaptation_strategy" />);
  const choices = await screen.findAllByRole("checkbox");
  expect(choices[0]).toHaveProperty("checked", true);
  fireEvent.click(choices[1]);
  fireEvent.change(screen.getByLabelText("目标集数"), { target: { value: "8" } });
  fireEvent.change(screen.getByLabelText("补充改编要求"), { target: { value: "Keep ending\nRetain conflict" } });
  fireEvent.click(screen.getByRole("button", { name: "确认方案并生成 Brief" }));
  await waitFor(() => expect(input.onResolve).toHaveBeenCalledWith(request, "select_adaptation_strategy", "", {
    adaptation_options_artifact_version_id: "av1",
    selection: { selected_option_ids: ["one", "two"], combined_methods: ["one", "two"], custom_changes: ["Keep ending", "Retain conflict"] },
    creation_config: { target_episode_count: 8, episode_duration_minutes: 2, user_requirements: ["Keep ending", "Retain conflict"] },
  }));
});

it.each(["volume_fit", "episode_checkpoint"])("preserves the %s structured confirmation", async (mode) => {
  const input = props();
  const request = approval({ scope: mode, options: ["approve"] });
  render(<ApprovalCard {...input} approval={request} viewKey={mode === "volume_fit" ? "volume_fit" : "action_list"} />);
  if (mode === "volume_fit") {
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "Preserve the plot" } });
    fireEvent.click(screen.getByRole("button", { name: "确认策略并继续" }));
  } else fireEvent.click(screen.getByRole("button", { name: "确认并自动生成剩余集" }));
  await waitFor(() => expect(input.onResolve).toHaveBeenCalledWith(request, "approve", "", mode === "volume_fit" ? { expansion_strategy: "Preserve the plot" } : { episode_execution_mode: "continuous" }));
});

function quality(): QualityReview {
  return { quality_review_id: "av1", review_version: 1, project_id: "p", run_id: "run", step_run_id: "step", input_snapshot_hash: "hash1", status: "action_required", scope: "set", issue_counts: { blocker: 1, high: 0, medium: 0, low: 0 }, recommended_route: "script_generation", affected_episode_nos: [1], result: {} };
}

it("keeps blocker risk acceptance hidden and submits the quality revision route", async () => {
  vi.spyOn(api, "getQualityReview").mockResolvedValue(quality());
  const input = props();
  const request = approval({ scope: "quality_review", options: ["accept_with_risk", "ai_revise", "confirm_change", "manual_edit"] });
  render(<ApprovalCard {...input} approval={request} viewKey="quality_review" />);
  await screen.findByText("阻断 1");
  expect(screen.queryByRole("button", { name: "确认保留风险" })).toBeNull();
  expect(screen.queryByRole("button", { name: "确认修改" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "让 Agent 返修" }));
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Resolve blocker" } });
  fireEvent.click(screen.getByRole("button", { name: "提交修改要求" }));
  await waitFor(() => expect(input.onResolve).toHaveBeenCalledWith(request, "ai_revise", "Resolve blocker", undefined));
});

it.each(["quality_review_id", "run_id", "input_snapshot_hash", "review_version"])("rejects a quality subject with mismatched %s", async (field) => {
  vi.spyOn(api, "getQualityReview").mockResolvedValue({ ...quality(), [field]: field === "review_version" ? 2 : "wrong" });
  render(<ApprovalCard {...props()} approval={approval({ scope: "quality_review", options: ["accept_with_risk"] })} viewKey="quality_review" />);
  await screen.findByRole("button", { name: "重试读取确认内容" });
  expect(screen.queryByRole("button", { name: "确认保留风险" })).toBeNull();
});
