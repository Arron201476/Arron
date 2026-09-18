import { captureDOMSelection, type SelectionCaptureBase } from "../contextTargeting";
import type { AgentTargetSelection } from "../types";
import { TextArtifactEditor } from "./TextArtifactEditor";

type Props = {
  payload: unknown;
  episodeNo: number;
  dirty: boolean;
  saving: boolean;
  locked?: boolean;
  saveStatus?: string;
  failure: string;
  onChange: (payload: unknown) => void;
  onSave: () => void;
  onCancel: () => void;
  selectionBase: SelectionCaptureBase;
  onSelectionChange: (selection: AgentTargetSelection | null) => void;
  highlightSpeakers?: boolean;
};

type ScriptScene = {
  scene_id: string;
  heading: string;
  blocks: Array<{ line_id: string; block_type: string; text: string; speaker?: string }>;
};

export function ScriptArtifactEditor({ payload, episodeNo, dirty, saving, locked, saveStatus, failure, onChange, onSave, onCancel, selectionBase, onSelectionChange, highlightSpeakers = false }: Props) {
  const record = asRecord(payload);
  const incomingText = scriptText(record);
  const commit = (nextText: string, html: string) => {
    onChange({
      ...record,
      episode_no: record.episode_no ?? episodeNo,
      script_text: nextText,
      editor_html: html,
      scenes: scenesFromScript(nextText, episodeNo),
    });
  };
  const captureSelection = (editor: HTMLDivElement) => {
    onSelectionChange(captureDOMSelection(editor, {
      ...selectionBase,
      fieldPath: "script_text",
      entity: { ...selectionBase.entity, episode_id: String(episodeNo || 1) },
    }));
  };

  return (
    <TextArtifactEditor editorID="script-body" editorLabel="剧本正文编辑器" toolbarLabel="剧本编辑工具栏"
      text={incomingText} dirty={dirty} saving={saving} locked={locked} saveStatus={saveStatus} failure={failure}
      header={<div className="script-editor-label">第 {episodeNo || 1} 集 · 剧本正文</div>}
      renderHTML={highlightSpeakers ? highlightedScriptHTML : undefined}
      onCommit={commit} onSelection={captureSelection} onSave={onSave} onCancel={onCancel} />
  );
}

function scriptText(payload: Record<string, unknown>) {
  if (typeof payload.script_text === "string") return payload.script_text;
  if (typeof payload.text === "string") return payload.text;
  return "";
}

function highlightedScriptHTML(text: string) {
  return text.split(/\r?\n/).map((line) => {
    if (!line) return "<div><br></div>";
    const escaped = escapeHTML(line);
    if (/^场\s*\d+\s*[-－]\s*\d+/.test(line.trim())) return `<div class="script-scene-heading">${escaped}</div>`;
    const dialogue = line.match(/^([^：:]{1,32})([：:])(.*)$/);
    if (!dialogue || /^(地点|人物|时间|场景|系统提示|屏幕文字)$/.test(dialogue[1].trim())) return `<div>${escaped}</div>`;
    return `<div class="script-dialogue-line"><span class="script-speaker-token">${escapeHTML(dialogue[1])}</span>${escapeHTML(dialogue[2])}${escapeHTML(dialogue[3])}</div>`;
  }).join("");
}

function escapeHTML(value: string) {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#039;");
}

export function scenesFromScript(text: string, episodeNo: number): ScriptScene[] {
  const scenes: ScriptScene[] = [];
  let scene: ScriptScene | null = null;
  const ensureScene = () => {
    if (scene) return scene;
    scene = { scene_id: `scene-${episodeNo || 1}-1`, heading: `第 ${episodeNo || 1} 集`, blocks: [] };
    scenes.push(scene);
    return scene;
  };

  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line) continue;
    if (/^第\s*\d+\s*集(?:\s|$)/.test(line)) continue;
    if (/^(?:场\s*)?\d+[-－—]\d+\b/.test(line) || /^场景\s*\d+/.test(line)) {
      scene = { scene_id: `scene-${episodeNo || 1}-${scenes.length + 1}`, heading: line, blocks: [] };
      scenes.push(scene);
      continue;
    }
    const current = ensureScene();
    const candidate = line.match(/^([^：:]{1,24})[：:]\s*(.+)$/);
    const dialogue = candidate && !/^(地点|人物|时间|场景|系统提示|屏幕文字)$/.test(candidate[1].trim()) ? candidate : null;
    current.blocks.push({
      line_id: `${current.scene_id}-line-${current.blocks.length + 1}`,
      block_type: dialogue ? "dialogue" : line.startsWith("△") ? "action" : "scene_note",
      ...(dialogue ? { speaker: dialogue[1].trim(), text: dialogue[2].trim() } : { text: line }),
    });
  }
  return scenes;
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}
