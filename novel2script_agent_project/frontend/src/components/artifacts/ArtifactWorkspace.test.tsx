import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Artifact } from "../../api/types";
import { ArtifactWorkspace } from "./ArtifactWorkspace";

const artifact: Artifact = {
  artifact_id: "artifact_story_v2", artifact_type: "story_bible", project_id: "project_1", run_id: "run_1", version: 2,
  status: "pending_approval", source_mode: "novel", created_at: "2026-01-01T00:00:00Z",
  payload: {
    generation_config_ref: { run_id: "run_internal", config_version: 3 },
    story_overview: { core_conflict: "新冲突" },
    characters: [{ character_id: "character_internal", name: "主角", goal: "目标" }],
    relationships: [],
    source_trace: { source_ref: "source_internal", from_source_text: [] },
  },
};

const sourceArtifact: Artifact = {
  ...artifact,
  artifact_id: "artifact_source_v1",
  artifact_type: "source_input",
  version: 1,
  payload: {
    generation_config_ref: { run_id: "run_1", version: 1 },
    text: "确认生成配置：2集，每集1.5分钟\n\n【附件：测试用书.txt】\n这是应当直接展示的输入材料正文。",
  },
};

describe("ArtifactWorkspace", () => {
  it("puts source material first without repeating generation config", () => {
    render(<ArtifactWorkspace artifacts={[sourceArtifact]} events={[]} inputText="" latestByType={new Map([["source_input", sourceArtifact]])} onArtifactSelect={() => {}} selectedArtifactType="source_input" setInputText={() => {}} sourceFiles={[]} sourcePreviewText="" visibleTypes={["source_input"]} view="artifacts" />);

    expect(screen.getByText("测试用书.txt")).toBeInTheDocument();
    expect(screen.getByText("这是应当直接展示的输入材料正文。")).toBeInTheDocument();
    expect(screen.queryByText("生成配置")).not.toBeInTheDocument();
    expect(screen.queryByText(/确认生成配置/)).not.toBeInTheDocument();
    expect(screen.queryByText(/【附件/)).not.toBeInTheDocument();
  });

  it("keeps one vertical outline while switching from reading to inline editing", () => {
    render(<ArtifactWorkspace artifacts={[artifact]} events={[]} inputText="" latestByType={new Map([["story_bible", artifact]])} onArtifactSelect={() => {}} selectedArtifactType="story_bible" setInputText={() => {}} sourceFiles={[]} sourcePreviewText="" visibleTypes={["story_bible"]} view="artifacts" />);
    const outline = screen.getByRole("navigation", { name: "产物大纲" });
    expect(screen.queryByText("run_internal")).not.toBeInTheDocument();
    expect(screen.queryByText("character_internal")).not.toBeInTheDocument();
    expect(screen.queryByText("source_internal")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("搜索产物内容"), { target: { value: "主角" } });
    expect(screen.getAllByText("人物").length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: /编辑内容/ }));
    expect(screen.queryByText("run_internal")).not.toBeInTheDocument();
    expect(screen.queryByText("character_internal")).not.toBeInTheDocument();
    expect(screen.queryByText("source_internal")).not.toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "产物大纲" })).toBe(outline);
    expect(screen.queryByRole("navigation", { name: "编辑字段目录" })).not.toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "删除" }).length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(screen.queryByRole("button", { name: "删除" })).not.toBeInTheDocument();
  });

  it("does not fall back to source input when the selected process artifact is absent", () => {
    render(<ArtifactWorkspace artifacts={[sourceArtifact]} events={[]} inputText="" latestByType={new Map([["source_input", sourceArtifact]])} onArtifactSelect={() => {}} selectedArtifactType="story_bible" setInputText={() => {}} sourceFiles={[]} sourcePreviewText="" visibleTypes={["source_input", "story_bible"]} view="artifacts" />);
    expect(screen.getByText("还没有该类产物。")).toBeInTheDocument();
    expect(screen.queryByText("这是应当直接展示的输入材料正文。")).not.toBeInTheDocument();
  });

  it("disables process artifact editing while downstream generation is running", () => {
	   render(<ArtifactWorkspace artifacts={[artifact]} events={[]} inputText="" latestByType={new Map([["story_bible", artifact]])} onArtifactSelect={() => {}} selectedArtifactType="story_bible" setInputText={() => {}} sourceFiles={[]} sourcePreviewText="" visibleTypes={["story_bible"]} view="artifacts" editingLocked />);
	   expect(screen.getByRole("button", { name: /编辑内容/ })).toBeDisabled();
	   expect(screen.getByText("当前正在生成下游内容，完成或暂停后可编辑。")).toBeInTheDocument();
	 });

  it("enables artifact export only while the workflow is idle", () => {
    const { rerender } = render(<ArtifactWorkspace artifacts={[artifact]} events={[]} inputText="" latestByType={new Map([["story_bible", artifact]])} onArtifactSelect={() => {}} projectTitle="项目1" selectedArtifactType="story_bible" setInputText={() => {}} sourceFiles={[]} sourcePreviewText="" visibleTypes={["story_bible"]} view="artifacts" />);
    expect(screen.getByRole("button", { name: "导出故事圣经" })).toBeEnabled();
    rerender(<ArtifactWorkspace artifacts={[artifact]} events={[]} exportLocked inputText="" latestByType={new Map([["story_bible", artifact]])} onArtifactSelect={() => {}} projectTitle="项目1" selectedArtifactType="story_bible" setInputText={() => {}} sourceFiles={[]} sourcePreviewText="" visibleTypes={["story_bible"]} view="artifacts" />);
    expect(screen.getByRole("button", { name: "导出故事圣经" })).toBeDisabled();
  });

  it("shows primary activity by default and keeps technical events available on demand", () => {
    const events = [
      { event_id: "event_1", run_id: "run_1", type: "run_started", message: "started" },
      { event_id: "event_2", run_id: "run_1", step_id: "step_build_story_bible", type: "step_started", message: "started step" },
      { event_id: "event_3", run_id: "run_1", step_id: "step_build_story_bible", type: "artifact_created", message: "created", payload: { artifact_type: "story_bible" } },
      { event_id: "event_4", run_id: "run_1", step_id: "step_build_story_bible", type: "step_completed", message: "completed step" },
      { event_id: "event_5", run_id: "run_1", type: "run_completed", message: "completed" },
    ];
    render(<ArtifactWorkspace artifacts={[artifact]} events={events} inputText="" latestByType={new Map([["story_bible", artifact]])} onArtifactSelect={() => {}} selectedArtifactType={null} setInputText={() => {}} sourceFiles={[]} sourcePreviewText="" visibleTypes={["story_bible"]} view="events" />);

    expect(screen.getByText("关键节点，共 3 条")).toBeInTheDocument();
    expect(screen.queryByText("执行步骤")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "全部记录" }));
    expect(screen.getByText("含系统步骤，共 5 条")).toBeInTheDocument();
    expect(screen.getByText("执行步骤")).toBeInTheDocument();
    expect(screen.getByText("完成步骤")).toBeInTheDocument();
  });
});
