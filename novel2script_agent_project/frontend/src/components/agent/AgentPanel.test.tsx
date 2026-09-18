import { createRef } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { AgentPanel } from "./AgentPanel";

describe("AgentPanel", () => {
  it("sends the generate-script shortcut through its dedicated action", () => {
    const generateShortcut = vi.fn();
    const send = vi.fn();

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
        onGenerateShortcut={generateShortcut}
        onKeepDownstream={vi.fn()}
        onOpenEvents={vi.fn()}
        onPause={vi.fn()}
        onRegenerateDownstream={vi.fn()}
        onRemoveAttachment={vi.fn()}
        onReset={vi.fn()}
        onResume={vi.fn()}
        onSend={send}
        onSourceModeChange={vi.fn()}
        onStop={vi.fn()}
        onSubmitGenerationConfig={vi.fn()}
        showGenerateShortcut
        sourceMode="novel"
        timelineRef={createRef<HTMLDivElement>()}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "生成剧本" }));

    expect(generateShortcut).toHaveBeenCalledOnce();
    expect(send).not.toHaveBeenCalled();
  });

  it("keeps completed run details behind one clear expansion control", () => {
    render(
      <AgentPanel
        activeApprovalID={null}
        attachments={[]}
        composerText=""
        fileInputRef={createRef<HTMLInputElement>()}
        isSendDisabled={false}
        isThinking={false}
        messages={[{
          id: "agent_1",
          role: "agent",
          text: "剧本已经生成完成。",
          steps: [
            { key: "prepare", label: "准备剧本" },
            { key: "write-1", label: "生成剧本" },
            { key: "write-2", label: "生成剧本" },
            { key: "finish", label: "整理剧本" },
          ],
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
        sourceMode="novel"
        timelineRef={createRef<HTMLDivElement>()}
      />,
    );

    expect(screen.getByText("已完成 4 步")).toBeInTheDocument();
    expect(screen.getByText("最后完成：整理剧本")).toBeInTheDocument();
    expect(screen.queryByText("准备剧本")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "查看过程记录" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "查看过程记录" }));
  });

  it("uses the detected episode count when preserving source episode markers", () => {
    const submit = vi.fn();
    render(
      <AgentPanel
        activeApprovalID={null}
        attachments={[]}
        composerText=""
        fileInputRef={createRef<HTMLInputElement>()}
        isSendDisabled={false}
        isThinking={false}
        messages={[{
          id: "agent_config",
          role: "agent",
          text: "请确认生成配置",
          generationConfigPrompt: {
            source_mode: "novel",
            original_message: "把附件改成短剧",
            file_ids: ["file_1"],
            defaults: {
              existing_episode_markers_detected: true,
              detected_episode_count: 14,
              episode_duration_minutes: 1.5,
            },
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
        onSubmitGenerationConfig={submit}
        runStatus="completed"
        sourceMode="novel"
        timelineRef={createRef<HTMLDivElement>()}
      />,
    );

    fireEvent.click(screen.getByLabelText("保留原分集"));
    expect(screen.getByLabelText("已识别原文分集数")).toHaveValue("14 集（已识别）");
    expect(screen.queryByRole("spinbutton", { name: "集数" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "确认并开始生成" }));
    expect(submit).toHaveBeenCalledWith("agent_config", "把附件改成短剧", expect.objectContaining({
      target_episode_count: 14,
      preserve_existing_episode_marks: true,
      existing_episode_markers_detected: true,
      detected_episode_count: 14,
    }), "novel", ["file_1"]);
  });
});
