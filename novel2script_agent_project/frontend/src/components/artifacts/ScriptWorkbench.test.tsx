import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Artifact } from "../../api/types";
import { ScriptWorkbench } from "./ScriptWorkbench";

const script: Artifact = {
  artifact_id: "script_1_v3", artifact_type: "script_unit", project_id: "project_1", run_id: "run_1", version: 3,
  status: "confirmed", source_mode: "novel", payload: { episode_id: 1, title: "第一集", scenes: [{ scene_id: "scene_1", heading: "内景", blocks: [{ line_id: "line_1", block_type: "dialogue", speaker: "主角", text: "同一句台词" }] }] },
};

describe("ScriptWorkbench", () => {
  it("renders the production Lexical editor with stable scene and line ids", async () => {
    render(<ScriptWorkbench artifacts={[script]} latestByType={new Map([["script_unit", script]])} />);
    expect(screen.getByRole("tab", { name: /第1集/ })).toHaveAttribute("aria-selected", "true");
    const editor = screen.getByLabelText("剧本正文编辑器");
    await waitFor(() => expect(editor.querySelector('[data-line-id="line_1"]')).toBeTruthy());
    expect(editor.querySelector('[data-scene-id="scene_1"]')).toBeTruthy();
    const dialogue = editor.querySelector<HTMLElement>('[data-line-id="line_1"]');
    expect(dialogue).toHaveAttribute("data-speaker", "主角");
    expect(dialogue).toHaveTextContent(/^同一句台词$/);
    expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
    expect(screen.queryByRole("combobox", { name: "行类型" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "下划线" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("button", { name: "删除线" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.queryByRole("button", { name: "添加批注" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "提出修改建议" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "接受选中建议" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "拒绝选中建议" })).not.toBeInTheDocument();
  });

  it("makes the script editor read-only while a model run is active", async () => {
	   render(<ScriptWorkbench artifacts={[script]} latestByType={new Map([["script_unit", script]])} editingLocked />);
	   expect(screen.getByText("当前正在生成下游内容，完成或暂停后可编辑剧本。")).toBeInTheDocument();
	   expect(screen.getByRole("button", { name: "只读聚合视图" })).toBeDisabled();
	   expect(screen.getByRole("button", { name: "撤销" })).toBeDisabled();
	   expect(screen.getByLabelText("剧本正文编辑器")).toHaveAttribute("aria-readonly", "true");
	 });

  it("exports only after every configured episode exists", () => {
    const { rerender } = render(<ScriptWorkbench artifacts={[script]} latestByType={new Map([["script_unit", script]])} projectTitle="项目1" targetEpisodeCount={2} />);
    expect(screen.getByRole("button", { name: "导出完整剧本" })).toBeDisabled();
    rerender(<ScriptWorkbench artifacts={[script]} latestByType={new Map([["script_unit", script]])} projectTitle="项目1" targetEpisodeCount={1} />);
    expect(screen.getByRole("button", { name: "导出完整剧本" })).toBeEnabled();
  });
});
