// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { VideoScriptArtifactEditor } from "./VideoScriptArtifactEditor";

afterEach(cleanup);

describe("VideoScriptArtifactEditor", () => {
  it("renders source video, scene navigation, summary and highlighted speakers", async () => {
    const { container } = render(<VideoScriptArtifactEditor
      payload={{
        episode_no: 1,
        source_file_name: "第一集.mp4",
        plot_summary: "主角在婚礼前发现真相。",
        script_text: "第1集\n\n场1-1 婚礼现场 日 内\nCynthia：我不同意。",
        source_refs: [{ asset_id: "ast_video_1" }],
        scenes: [{ scene_id: "scene_1", heading: "场1-1 婚礼现场 日 内", time_range: { start_ms: 1000, end_ms: 5200 } }],
        extraction_completeness: { status: "complete_first_pass" },
      }}
      episodeNo={1}
      dirty={false}
      saving={false}
      failure=""
      onChange={vi.fn()}
      onSave={vi.fn()}
      onCancel={vi.fn()}
      selectionBase={{ artifactID: "artifact_1", artifactType: "video_script_unit", artifactLabel: "视频还原剧本" }}
      onSelectionChange={vi.fn()}
    />);

    expect(screen.getByText("第一集.mp4")).toBeTruthy();
    expect(screen.getByText("主角在婚礼前发现真相。")).toBeTruthy();
    expect(screen.getByRole("button", { name: /场1-1 婚礼现场/ })).toBeTruthy();
    const sceneStrip = container.querySelector(".video-scene-strip");
    expect(sceneStrip).toBeTruthy();
    expect(sceneStrip?.parentElement?.classList.contains("video-script-editor-pane")).toBe(true);
    expect(sceneStrip?.nextElementSibling?.classList.contains("script-editor-shell")).toBe(true);
    expect(container.querySelector("video")?.getAttribute("src")).toBe("/api/v1/assets/ast_video_1/content");
    await waitFor(() => expect(container.querySelector(".script-speaker-token")?.textContent).toBe("Cynthia"));

    const video = container.querySelector("video") as HTMLVideoElement;
    const play = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(video, "play", { configurable: true, value: play });
    const sceneHeading = container.querySelector(".script-scene-heading") as HTMLElement;
    const scrollIntoView = vi.fn();
    Object.defineProperty(sceneHeading, "scrollIntoView", { configurable: true, value: scrollIntoView });
    fireEvent.click(screen.getByRole("button", { name: /场1-1 婚礼现场/ }));
    fireEvent(video, new Event("loadedmetadata"));

    expect(video.currentTime).toBe(1);
    expect(play).toHaveBeenCalledOnce();
    expect(sceneHeading.classList.contains("active-scene")).toBe(true);
    expect(scrollIntoView).toHaveBeenCalledWith({ behavior: "smooth", block: "start" });
  });

  it("renders scene timestamps from video source refs", () => {
    render(<VideoScriptArtifactEditor
      payload={{
        source_file_name: "long-video.mp4",
        source_refs: [{ asset_id: "ast_video_1" }],
        script_text: "第1集",
        scenes: [{
          scene_id: "scene_1",
          heading: "工厂门口",
          source_refs: [{
            source_type: "video_time_range",
            time_range: { start_ms: 62_000, end_ms: 99_000 },
          }],
        }],
      }}
      episodeNo={1}
      dirty={false}
      saving={false}
      failure=""
      onChange={vi.fn()}
      onSave={vi.fn()}
      onCancel={vi.fn()}
      selectionBase={{ artifactID: "artifact_1", artifactType: "video_script_unit", artifactLabel: "视频还原剧本" }}
      onSelectionChange={vi.fn()}
    />);

    expect(screen.getByText("01:02 - 01:39")).toBeTruthy();
    expect(screen.getByRole("button", { name: "工厂门口 01:02 至 01:39" })).toBeTruthy();
  });

  it("recovers a missing source ref from one available video with the same filename", () => {
    const { container } = render(<VideoScriptArtifactEditor
      payload={{
        episode_no: 1,
        source_file_name: "shot_01.mp4",
        source_refs: [{ source_type: "user_message", message_id: "message_1" }],
        script_text: "第1集",
      }}
      projectAssets={[{
        asset_id: "ast_video_fallback",
        current_snapshot_id: "snapshot_1",
        kind: "video",
        display_name: "shot_01.mp4",
        original_filename: "shot_01.mp4",
        status: "available",
        parse_status: "pending",
      }]}
      episodeNo={1}
      dirty={false}
      saving={false}
      failure=""
      onChange={vi.fn()}
      onSave={vi.fn()}
      onCancel={vi.fn()}
      selectionBase={{ artifactID: "artifact_1", artifactType: "video_script_unit", artifactLabel: "视频还原剧本" }}
      onSelectionChange={vi.fn()}
    />);

    expect(container.querySelector("video")?.getAttribute("src")).toBe("/api/v1/assets/ast_video_fallback/content");
    expect(screen.queryByText("未找到原视频引用")).toBeNull();
  });

  it("does not guess when duplicate videos share the same filename", () => {
    const shared = {
      current_snapshot_id: "snapshot_1",
      kind: "video",
      display_name: "shot_01.mp4",
      original_filename: "shot_01.mp4",
      status: "available",
      parse_status: "pending",
    };
    const { container } = render(<VideoScriptArtifactEditor
      payload={{ source_file_name: "shot_01.mp4", script_text: "第1集" }}
      projectAssets={[{ ...shared, asset_id: "ast_video_1" }, { ...shared, asset_id: "ast_video_2" }]}
      episodeNo={1}
      dirty={false}
      saving={false}
      failure=""
      onChange={vi.fn()}
      onSave={vi.fn()}
      onCancel={vi.fn()}
      selectionBase={{ artifactID: "artifact_1", artifactType: "video_script_unit", artifactLabel: "视频还原剧本" }}
      onSelectionChange={vi.fn()}
    />);

    expect(container.querySelector("video")).toBeNull();
    expect(screen.getByText("未找到原视频引用")).toBeTruthy();
  });

  it("falls back to a browser-compatible preview after a decode error", () => {
    const { container } = render(<VideoScriptArtifactEditor
      payload={{ source_file_name: "hevc.mp4", source_refs: [{ asset_id: "ast_hevc" }], script_text: "第1集" }}
      episodeNo={1}
      dirty={false}
      saving={false}
      failure=""
      onChange={vi.fn()}
      onSave={vi.fn()}
      onCancel={vi.fn()}
      selectionBase={{ artifactID: "artifact_1", artifactType: "video_script_unit", artifactLabel: "视频还原剧本" }}
      onSelectionChange={vi.fn()}
    />);

    const original = container.querySelector("video") as HTMLVideoElement;
    expect(original.getAttribute("src")).toBe("/api/v1/assets/ast_hevc/content");
    fireEvent.error(original);

    const compatible = container.querySelector("video") as HTMLVideoElement;
    expect(compatible.getAttribute("src")).toBe("/api/v1/assets/ast_hevc/content?browser_compatible=1");
    expect(screen.getByText("正在生成兼容预览...")).toBeTruthy();
    fireEvent.error(compatible);
    expect(screen.getByText("视频预览生成失败")).toBeTruthy();
  });
});
