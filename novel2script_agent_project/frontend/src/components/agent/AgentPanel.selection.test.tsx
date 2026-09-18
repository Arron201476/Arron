import { createRef } from "react";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { AgentPanel } from "./AgentPanel";

describe("AgentPanel script range selection", () => {
  it("describes a cross-scene range without exposing line internals", () => {
    render(
      <AgentPanel
        activeApprovalID={null}
        attachments={[]}
        composerText=""
        fileInputRef={createRef<HTMLInputElement>()}
        isSendDisabled={false}
        isThinking={false}
        messages={[]}
        onAddFiles={vi.fn()}
        onApprove={vi.fn()}
        onClearSelection={vi.fn()}
        onCollapse={vi.fn()}
        onComposerChange={vi.fn()}
        onGenerateShortcut={vi.fn()}
        onKeepDownstream={vi.fn()}
        onOpenEvents={vi.fn()}
        onPause={vi.fn()}
        onRegenerateDownstream={vi.fn()}
        onRemoveAttachment={vi.fn()}
        onReset={vi.fn()}
        onResume={vi.fn()}
        onSend={vi.fn()}
        onSourceModeChange={vi.fn()}
        onStop={vi.fn()}
        onSubmitGenerationConfig={vi.fn()}
        runStatus="completed"
        selectionContext={{
          artifact_id: "artifact_scripts",
          artifact_type: "scripts",
          version: 3,
          episode_id: 1,
          start_scene_id: "scene_1",
          end_scene_id: "scene_2",
          start_line_id: "line_1",
          end_line_id: "line_3",
          line_ids: ["line_1", "line_2", "line_3"],
          selection_scope: "range",
          selected_text: "第一行\n第二行\n第三行",
        }}
        sourceMode="novel"
        timelineRef={createRef<HTMLDivElement>()}
      />,
    );

    expect(screen.getByText("第 1 集 / 3 行 / 跨场景")).toBeInTheDocument();
    expect(screen.getByText("第一行 第二行 第三行")).toBeInTheDocument();
  });

  it("keeps a sent selection reference in the user message", () => {
    render(
      <AgentPanel
        activeApprovalID={null}
        attachments={[]}
        composerText=""
        fileInputRef={createRef<HTMLInputElement>()}
        isSendDisabled={false}
        isThinking={false}
        messages={[{
          id: "user_selected",
          role: "user",
          text: "情绪更丰富一些",
          selectionContext: {
            artifact_id: "artifact_script_1",
            artifact_type: "script_unit",
            version: 3,
            episode_id: 1,
            scene_id: "第1场",
            node_id: "line_8",
            selection_scope: "line",
            block_type: "dialogue",
            selected_text: "别打了！都是一家人！",
          },
        }]}
        onAddFiles={vi.fn()}
        onApprove={vi.fn()}
        onClearSelection={vi.fn()}
        onCollapse={vi.fn()}
        onComposerChange={vi.fn()}
        onGenerateShortcut={vi.fn()}
        onKeepDownstream={vi.fn()}
        onOpenEvents={vi.fn()}
        onPause={vi.fn()}
        onRegenerateDownstream={vi.fn()}
        onRemoveAttachment={vi.fn()}
        onReset={vi.fn()}
        onResume={vi.fn()}
        onSend={vi.fn()}
        onSourceModeChange={vi.fn()}
        onStop={vi.fn()}
        onSubmitGenerationConfig={vi.fn()}
        runStatus="completed"
        selectionContext={null}
        sourceMode="novel"
        timelineRef={createRef<HTMLDivElement>()}
      />,
    );

    expect(screen.getByLabelText("已发送的选区引用")).toBeInTheDocument();
    expect(screen.getByText("引用选区")).toBeInTheDocument();
    expect(screen.getByText("v3")).toBeInTheDocument();
    expect(screen.getByText("别打了！都是一家人！")).toBeInTheDocument();
    expect(screen.queryByText("已引用选区")).not.toBeInTheDocument();
  });
});
