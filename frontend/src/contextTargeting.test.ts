// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { canonicalJSONStringify, captureDOMSelection, episodeNumberFromScope, messageContextPayload, resolveViewedCapabilityID } from "./contextTargeting";
import type { AgentComposerContext } from "./types";

describe("Agent context targeting", () => {
  it("recognizes only explicit positive episode scopes", () => {
    expect(episodeNumberFromScope("episode:12")).toBe(12);
    expect(episodeNumberFromScope("ep-3")).toBe(3);
    expect(episodeNumberFromScope("global")).toBeNull();
    expect(episodeNumberFromScope("project:0")).toBeNull();
    expect(episodeNumberFromScope(null)).toBeNull();
  });

  it("serializes object keys deterministically", () => {
    expect(canonicalJSONStringify({ z: 1, a: { y: 2, b: 3 } })).toBe('{"a":{"b":3,"y":2},"z":1}');
  });

  it("does not borrow the active Skill capability for a historical artifact", () => {
    const activities = [
      { run_id: "run-video", capability_id: "video_reference_creation" },
      { run_id: "run-story", capability_id: "non_novel_story" },
    ] as never[];

    expect(resolveViewedCapabilityID("run-story", activities, "run-video", "video_reference_creation"))
      .toBe("non_novel_story");
    expect(resolveViewedCapabilityID("run-unknown", activities, "run-video", "video_reference_creation"))
      .toBeNull();
  });

  it("captures an immutable selection with line metadata", () => {
    document.body.innerHTML = '<div id="root"><span data-field-path="script_text" data-episode-id="1" data-line-id="line-2" data-location-label="第 1 集 · 第 2 行">前文目标台词后文</span></div>';
    const root = document.querySelector<HTMLElement>("#root")!;
    const text = root.querySelector("span")!.firstChild!;
    const range = document.createRange();
    range.setStart(text, 2);
    range.setEnd(text, 6);
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);

    const result = captureDOMSelection(root, {
      artifactID: "artifact-1", artifactType: "script_unit", artifactLabel: "第 1 集", scopeKey: "episode:1",
    });

    expect(result).toMatchObject({
      artifact_id: "artifact-1", artifact_type: "script_unit", target_scope: "selection",
      scope_key: "episode:1", field_path: "script_text",
      entity: { episode_id: "1", line_id: "line-2" },
      text_range: { selected_text: "目标台词" },
      display: { location_label: "第 1 集 · 第 2 行" },
    });
  });

  it("builds current-view references and hashes the selection", async () => {
    vi.stubGlobal("crypto", { subtle: { digest: vi.fn().mockResolvedValue(new Uint8Array(32).buffer) } });
    const context = {
      view: {
        project_id: "project-1", run_id: "run-1", capability_id: "novel_to_script", artifact_id: "artifact-1",
        artifact_version_id: "version-1", artifact_type: "script_unit", scope_key: "episode:1", artifact_label: "第 1 集",
        asset_set_version_id: "asset-set-version-1",
      },
      selection: {
        schema_version: "1.0.0", artifact_id: "artifact-1", artifact_type: "script_unit", target_scope: "selection",
        display: { artifact_label: "第 1 集", selected_text_summary: "目标台词" },
      },
    } as AgentComposerContext;

    const payload = await messageContextPayload(context);

    expect(payload.client_context).toMatchObject({
      current_artifact_id: "artifact-1", current_artifact_version_id: "version-1", viewed_run_id: "run-1",
      viewed_capability_id: "novel_to_script", current_scope_key: "episode:1",
      current_asset_set_version_id: "asset-set-version-1",
    });
    expect(payload.selection_snapshot).toMatchObject({ artifact_version_id: "version-1", snapshot_hash: "0".repeat(64) });
  });
});
