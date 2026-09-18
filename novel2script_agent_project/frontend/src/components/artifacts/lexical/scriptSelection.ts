import type { Artifact, ScriptBlockType, SelectionContext } from "../../../api/types";

export interface ScriptSelectionLine {
  episodeId: string;
  sceneId: string;
  lineId: string;
  blockType: ScriptBlockType;
  text: string;
}

interface ScriptSelectionPoint {
  lineId: string;
  offset: number;
}

export function buildScriptSelectionContext(
  artifact: Artifact,
  lines: ScriptSelectionLine[],
  anchor: ScriptSelectionPoint,
  focus: ScriptSelectionPoint,
): SelectionContext | null {
  const anchorIndex = lines.findIndex((line) => line.lineId === anchor.lineId);
  const focusIndex = lines.findIndex((line) => line.lineId === focus.lineId);
  if (anchorIndex < 0 || focusIndex < 0) return null;
  if (lines[anchorIndex].episodeId !== lines[focusIndex].episodeId) return null;

  const anchorOffset = clampOffset(anchor.offset, lines[anchorIndex].text.length);
  const focusOffset = clampOffset(focus.offset, lines[focusIndex].text.length);
  const forward = anchorIndex < focusIndex || (anchorIndex === focusIndex && anchorOffset <= focusOffset);
  const startIndex = forward ? anchorIndex : focusIndex;
  const endIndex = forward ? focusIndex : anchorIndex;
  const selectionStart = forward ? anchorOffset : focusOffset;
  const selectionEnd = forward ? focusOffset : anchorOffset;
  const selectedLines = lines.slice(startIndex, endIndex + 1);
  const selectedSegments = selectedLines.map((line, index) => {
    const start = index === 0 ? selectionStart : 0;
    const end = index === selectedLines.length - 1 ? selectionEnd : line.text.length;
    return line.text.slice(start, end);
  });
  const selectedText = selectedSegments.join("\n");
  if (!selectedText) return null;

  const first = selectedLines[0];
  const last = selectedLines[selectedLines.length - 1];
  const lineIDs = selectedLines.map((line) => line.lineId);
  const singleLine = lineIDs.length === 1;
  return {
    artifact_id: artifact.artifact_id,
    artifact_type: artifact.artifact_type,
    version: artifact.version,
    episode_id: first.episodeId,
    scene_id: singleLine ? first.sceneId : undefined,
    line_id: singleLine ? first.lineId : undefined,
    node_id: singleLine ? first.lineId : undefined,
    start_scene_id: first.sceneId,
    end_scene_id: last.sceneId,
    start_line_id: first.lineId,
    end_line_id: last.lineId,
    line_ids: lineIDs,
    selection_scope: singleLine ? "line" : "range",
    block_type: singleLine ? first.blockType : undefined,
    selection_start: selectionStart,
    selection_end: selectionEnd,
    selected_text: selectedText,
    before_context: first.text.slice(Math.max(0, selectionStart - 120), selectionStart),
    after_context: last.text.slice(selectionEnd, selectionEnd + 120),
    selection_source: "lexical_script_editor",
    selection_hash: selectionHash(artifact, lineIDs, selectionStart, selectionEnd, selectedText),
  };
}

function clampOffset(offset: number, length: number) {
  if (!Number.isFinite(offset)) return 0;
  return Math.max(0, Math.min(length, Math.round(offset)));
}

function selectionHash(artifact: Artifact, lineIDs: string[], start: number, end: number, text: string) {
  const input = `${artifact.artifact_id}:${artifact.version}:${lineIDs.join(",")}:${start}:${end}:${text}`;
  let hash = 2166136261;
  for (let index = 0; index < input.length; index++) hash = Math.imul(hash ^ input.charCodeAt(index), 16777619);
  return `fnv1a_${(hash >>> 0).toString(16).padStart(8, "0")}`;
}
