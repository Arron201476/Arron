import { describe, expect, it } from "vitest";
import type { Artifact } from "../../../api/types";
import { buildScriptSelectionContext, type ScriptSelectionLine } from "./scriptSelection";

const artifact = { artifact_id: "script_v3", artifact_type: "script_unit", version: 3 } as Artifact;
const lines: ScriptSelectionLine[] = [
  { episodeId: "1", sceneId: "scene_1", lineId: "line_1", blockType: "action", text: "第一行内容" },
  { episodeId: "1", sceneId: "scene_1", lineId: "line_2", blockType: "dialogue", text: "第二行内容" },
  { episodeId: "1", sceneId: "scene_2", lineId: "line_3", blockType: "action", text: "第三行内容" },
];

describe("buildScriptSelectionContext", () => {
  it("keeps precise offsets for a partial single-line selection", () => {
    const result = buildScriptSelectionContext(artifact, lines, { lineId: "line_1", offset: 1 }, { lineId: "line_1", offset: 4 });
    expect(result).toMatchObject({ line_id: "line_1", start_line_id: "line_1", end_line_id: "line_1", selection_start: 1, selection_end: 4, selected_text: "一行内", selection_scope: "line" });
  });

  it("captures an ordered cross-scene range without losing line ids", () => {
    const result = buildScriptSelectionContext(artifact, lines, { lineId: "line_1", offset: 2 }, { lineId: "line_3", offset: 2 });
    expect(result).toMatchObject({
      start_scene_id: "scene_1", end_scene_id: "scene_2", start_line_id: "line_1", end_line_id: "line_3",
      line_ids: ["line_1", "line_2", "line_3"], selection_start: 2, selection_end: 2,
      selected_text: "行内容\n第二行内容\n第三", selection_scope: "range",
    });
  });

  it("normalizes a reverse drag into document order", () => {
    const result = buildScriptSelectionContext(artifact, lines, { lineId: "line_3", offset: 2 }, { lineId: "line_1", offset: 2 });
    expect(result?.line_ids).toEqual(["line_1", "line_2", "line_3"]);
    expect(result?.selected_text).toBe("行内容\n第二行内容\n第三");
  });
});
