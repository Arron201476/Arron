import { useEffect, useMemo, useState } from "react";
import { Download } from "lucide-react";
import { updateArtifact } from "../../api/client";
import { currentArtifactFromConflict } from "../../api/errors";
import type { Artifact, ArtifactType, ArtifactUpdateResponse, ScriptBlockType, SelectionContext } from "../../api/types";
import { EmptyState } from "../ui/EmptyState";
import { ScriptLexicalEditor, type LexicalScriptDraft } from "./lexical/ScriptLexicalEditor";
import { downloadTextFile } from "../../lib/textDownload";

interface ScriptWorkbenchProps {
  artifacts: Artifact[];
  latestByType: Map<ArtifactType, Artifact>;
  onArtifactUpdated?: (payload: ArtifactUpdateResponse) => void;
  onArtifactConflict?: (artifact: Artifact) => void;
  onEditStateChange?: (state: { artifactId: string; artifactType: ArtifactType; editing: boolean; dirty: boolean } | null) => void;
  onEditSaveComplete?: (saved: boolean) => void;
  onSelectionChange?: (selection: SelectionContext | null) => void;
  editingLocked?: boolean;
  exportLocked?: boolean;
  projectTitle?: string;
  targetEpisodeCount?: number;
}

interface EpisodeDraft {
  artifact: Artifact;
  cacheKey: string;
  episodeId: string;
  readOnly?: boolean;
  title: string;
  status: string;
  scenes: SceneDraft[];
  sourceCollection?: "script_units" | "episodes" | "single";
  sourceIndex?: number;
}

interface SceneDraft {
  sceneId: string;
  heading: string;
  lines: LineDraft[];
}

interface LineDraft {
  lineId: string;
  blockType: ScriptBlockType;
  text: string;
  speaker?: string;
}

export function ScriptWorkbench({ artifacts, latestByType, onArtifactUpdated, onArtifactConflict, onEditStateChange, onEditSaveComplete, onSelectionChange, editingLocked = false, exportLocked = false, projectTitle = "未命名作品", targetEpisodeCount = 0 }: ScriptWorkbenchProps) {
  const episodes = useMemo(() => buildEpisodes(artifacts, latestByType), [artifacts, latestByType]);
  const [selectedEpisodeId, setSelectedEpisodeId] = useState("");
  const [saveStatus, setSaveStatus] = useState("");
  const [isSaving, setIsSaving] = useState(false);
  const [dirty, setDirty] = useState(false);
  const allEpisodesReady = useMemo(() => areAllEpisodesReady(episodes, targetEpisodeCount), [episodes, targetEpisodeCount]);
  const exportDisabled = exportLocked || dirty || isSaving || !allEpisodesReady;

  useEffect(() => {
    if (!episodes.length) return;
    setSelectedEpisodeId((current) => (episodes.some((episode) => episode.episodeId === current) ? current : episodes[0].episodeId));
  }, [episodes]);

  const selectedEpisode = episodes.find((episode) => episode.episodeId === selectedEpisodeId) || episodes[0];
  useEffect(() => {
    setSaveStatus("");
    onSelectionChange?.(null);
  }, [selectedEpisode?.cacheKey, onSelectionChange]);

  useEffect(() => {
    const preventLoss = (event: BeforeUnloadEvent) => {
      if (!dirty) return;
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", preventLoss);
    return () => window.removeEventListener("beforeunload", preventLoss);
  }, [dirty]);

  if (!episodes.length) {
    return <EmptyState>还没有剧本。确认分集卡后，Agent 才会继续生成剧本。</EmptyState>;
  }

  async function saveCurrentEpisode(draft: LexicalScriptDraft) {
	if (!selectedEpisode || selectedEpisode.readOnly || editingLocked || !dirty) return;
    setIsSaving(true);
    setSaveStatus("");
    try {
      const payload = {
        ...selectedEpisode.artifact.payload,
        episode_id: selectedEpisode.episodeId,
        title: draft.title || selectedEpisode.title,
        scenes: draft.scenes,
        script_text: draft.scriptText,
        editor_state: draft.editorState,
      };
      const response = await updateArtifact(selectedEpisode.artifact.artifact_id, { base_version: selectedEpisode.artifact.version, payload });
      onArtifactUpdated?.(response);
      setDirty(false);
      onEditStateChange?.(null);
      onEditSaveComplete?.(true);
      setSaveStatus("已保存");
    } catch (error) {
      const currentArtifact = currentArtifactFromConflict(error);
      if (currentArtifact) {
        setDirty(false);
        onEditStateChange?.(null);
        onArtifactConflict?.(currentArtifact);
      }
      onEditSaveComplete?.(false);
      setSaveStatus(error instanceof Error ? error.message : String(error));
    } finally {
      setIsSaving(false);
    }
  }

  return (
    <div className="script-editor-layout">
      <aside className="episode-rail" aria-label="分集目录" role="tablist">
        <div className="episode-rail-inner">
          <h2>分集目录</h2>
          <div className="episode-row-list">
            {episodes.map((episode) => (
              <button
                className={`episode-row ${episode.episodeId === selectedEpisode.episodeId ? "active" : ""}`}
                aria-label={`第${episode.episodeId}集，${episode.status}`}
                aria-selected={episode.episodeId === selectedEpisode.episodeId}
                key={episode.episodeId}
                onClick={() => {
                  if (dirty) {
                    setSaveStatus("当前集有未保存修改，请先保存或撤销后再切换。");
                    return;
                  }
                  setSelectedEpisodeId(episode.episodeId);
                  onSelectionChange?.(null);
                }}
                role="tab"
                type="button"
              >
                <span>第{episode.episodeId}集</span>
              </button>
            ))}
          </div>
        </div>
      </aside>

      <section className="script-editor-main">
		{editingLocked ? <p className="artifact-edit-lock" role="status">当前正在生成下游内容，完成或暂停后可编辑剧本。</p> : null}
        <ScriptLexicalEditor
          artifact={selectedEpisode.artifact}
          dirty={dirty}
          episodeId={selectedEpisode.episodeId}
          initialEditorState={Object.keys(readObject(selectedEpisode.artifact.payload.editor_state)).length ? readObject(selectedEpisode.artifact.payload.editor_state) : undefined}
          isSaving={isSaving}
          key={selectedEpisode.cacheKey}
          onDirtyChange={(nextDirty) => {
            setDirty(nextDirty);
            setSaveStatus(nextDirty ? "未保存" : "已保存");
            onEditStateChange?.(nextDirty ? { artifactId: selectedEpisode.artifact.artifact_id, artifactType: "script_unit", editing: true, dirty: true } : null);
          }}
          onSave={(draft) => void saveCurrentEpisode(draft)}
          onSelectionChange={onSelectionChange}
		  readOnly={selectedEpisode.readOnly || editingLocked}
          saveStatus={saveStatus}
          scenes={selectedEpisode.scenes}
          title={selectedEpisode.title}
          toolbarEnd={(
            <button
              aria-label="导出完整剧本"
              className="script-toolbar-export"
              disabled={exportDisabled}
              onClick={() => downloadTextFile(`${projectTitle}_剧本`, formatScriptExport(episodes))}
              title={!allEpisodesReady ? "所有集生成完成后可导出" : exportLocked ? "当前正在生成或修改剧本，完成后可导出" : dirty ? "请先保存当前修改" : "导出完整剧本为 TXT"}
              type="button"
            >
              <Download size={15} />
              <span>导出完整剧本</span>
            </button>
          )}
        />
      </section>
    </div>
  );
}

function buildEpisodes(artifacts: Artifact[], latestByType: Map<ArtifactType, Artifact>) {
  const scriptUnits = latestScriptUnitsByEpisode(artifacts);
  if (scriptUnits.length) return scriptUnits.map((artifact) => scriptUnitToEpisode(artifact)).sort(compareEpisode);

  const scriptUnit = latestByType.get("script_unit");
  if (scriptUnit && isActiveArtifact(scriptUnit)) return [scriptUnitToEpisode(scriptUnit)];

  const scripts = latestByType.get("scripts");
  if (scripts) {
    const aggregatedUnits = readArray(scripts.payload.script_units);
    const episodeItems = aggregatedUnits.length ? aggregatedUnits : readArray(scripts.payload.episodes);
    if (episodeItems.length) {
      return episodeItems.map((item, index) => {
        const unit = readObject(item);
        return scriptUnitToEpisode(
          {
            ...scripts,
            artifact_type: "script_unit",
            status: readString(unit.status) || "confirmed",
            version: Number(unit.version) || scripts.version,
            payload: unit,
          } as Artifact,
          aggregatedUnits.length ? "script_units" : "episodes",
          index,
          true,
        );
      });
    }
    const scriptText = readString(scripts.payload.script_text || scripts.payload.text);
    if (scriptText) return [scriptTextToEpisode(scripts, scriptText, "1", undefined, "single", undefined, true)];
  }

  return [];
}

function latestScriptUnitsByEpisode(artifacts: Artifact[]) {
  const map = new Map<string, Artifact>();
  for (const artifact of artifacts) {
    if (artifact.artifact_type !== "script_unit" || !isActiveArtifact(artifact)) continue;
    const episodeId = readString(artifact.payload?.episode_id) || artifact.artifact_id;
    const current = map.get(episodeId);
    if (!current || artifact.version >= current.version) {
      map.set(episodeId, artifact);
    }
  }
  return Array.from(map.values());
}

function areAllEpisodesReady(episodes: EpisodeDraft[], targetEpisodeCount: number) {
  if (targetEpisodeCount <= 0 || episodes.length !== targetEpisodeCount) return false;
  const ids = episodes.map((episode) => Number(episode.episodeId)).sort((left, right) => left - right);
  return ids.every((episodeID, index) => episodeID === index + 1)
    && episodes.every((episode) => !["draft", "failed", "invalidated", "superseded"].includes(episode.artifact.status));
}

function formatScriptExport(episodes: EpisodeDraft[]) {
  return episodes
    .slice()
    .sort(compareEpisode)
    .map((episode) => {
      const sourceText = readString(episode.artifact.payload.script_text || episode.artifact.payload.text).trim();
      if (sourceText) return sourceText;
      const scenes = episode.scenes.map((scene) => ({
        heading: scene.heading,
        blocks: scene.lines.map((line) => ({ text: line.text, speaker: line.speaker })),
      }));
      return scriptTextFromScenes(episode.title, scenes);
    })
    .join("\n\n==============================\n\n") + "\n";
}

function isActiveArtifact(artifact: Artifact) {
  return artifact.status !== "superseded" && artifact.status !== "invalidated";
}

function scriptUnitToEpisode(artifact: Artifact, sourceCollection?: "script_units" | "episodes", sourceIndex?: number, readOnly = false): EpisodeDraft {
  const payload = artifact.payload || {};
  const episodeId = readString(payload.episode_id) || readString(payload.episode_no) || "1";
  const title = readString(payload.title) || `第 ${episodeId} 集`;
  const scenes = readArray(payload.scenes).map((item, index) => sceneToDraft(item, index));
  const scriptText = readString(payload.script_text || payload.text);
  if (!scenes.length && scriptText) return scriptTextToEpisode(artifact, scriptText, episodeId, title, sourceCollection, sourceIndex, readOnly);
  return {
    artifact,
    cacheKey: `${artifact.artifact_id}@${artifact.version}`,
    episodeId,
    readOnly,
    title,
    status: artifact.status === "confirmed" ? "已生成" : "草稿",
    sourceCollection,
    sourceIndex,
    scenes: scenes.length
      ? scenes
      : [
          {
            sceneId: "empty-scene",
            heading: title,
            lines: [{ lineId: "empty-line", blockType: "scene_note" as ScriptBlockType, text: JSON.stringify(payload, null, 2) }],
          },
        ],
  };
}

function scriptTextToEpisode(
  artifact: Artifact,
  scriptText: string,
  episodeId: string,
  title = `第 ${episodeId} 集`,
  sourceCollection?: "script_units" | "episodes" | "single",
  sourceIndex?: number,
  readOnly = false,
): EpisodeDraft {
  const lines = scriptText
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line, index) => {
      const blockType = inferBlockType(line);
      const dialogue = blockType === "dialogue" ? line.match(/^([^：:]{1,24})[：:]\s*(.*)$/) : null;
      return {
        lineId: `text-${episodeId}-${index + 1}`,
        blockType,
        text: dialogue ? dialogue[2] : line,
        speaker: dialogue ? dialogue[1].trim() : undefined,
      };
    });
  return {
    artifact,
    cacheKey: `${artifact.artifact_id}@${artifact.version}`,
    episodeId,
    readOnly,
    title,
    status: artifact.status === "confirmed" ? "已生成" : "草稿",
    sourceCollection,
    sourceIndex,
    scenes: [
      {
        sceneId: `text-scene-${episodeId}`,
        heading: title,
        lines: lines.length ? lines : [{ lineId: `text-${episodeId}-empty`, blockType: "scene_note", text: "暂无剧本文本。" }],
      },
    ],
  };
}

function buildScriptSavePayload(episode: EpisodeDraft, editorHTML: string): Record<string, unknown> {
  const payload = { ...episode.artifact.payload };
  const scenes = scenesFromEditorHTML(editorHTML, episode, payload);
  payload.scenes = scenes;
  payload.script_text = scriptTextFromScenes(episode.title, scenes);
  payload.editor_html = editorHTML;
  payload.episode_id = readString(payload.episode_id) || episode.episodeId;
  payload.title = readString(payload.title) || episode.title;
  return payload;
}

function scenesFromEditorHTML(editorHTML: string, episode: EpisodeDraft, payload: Record<string, unknown>) {
  const template = document.createElement("template");
  template.innerHTML = editorHTML;
  const existingScenes = new Map(readArray(payload.scenes).map((value) => {
    const scene = readObject(value);
    return [readString(scene.scene_id), scene] as const;
  }));
  const sections = Array.from(template.content.querySelectorAll<HTMLElement>("[data-scene-id]"));
  const sourceSections = sections.length ? sections : [template.content as unknown as HTMLElement];
  return sourceSections.map((section, sceneIndex) => {
    const sceneId = section.getAttribute?.("data-scene-id") || episode.scenes[sceneIndex]?.sceneId || `scene-${episode.episodeId}-${sceneIndex + 1}`;
    const existingScene = existingScenes.get(sceneId) || {};
    const heading = section.querySelector?.("h3")?.textContent?.trim() || episode.scenes[sceneIndex]?.heading || `场景 ${sceneIndex + 1}`;
    const paragraphs = Array.from(section.querySelectorAll?.("p") || []);
    const blocks = paragraphs.map((paragraph, lineIndex) => {
      const lineId = paragraph.getAttribute("data-line-id") || `${sceneId}-line-${lineIndex + 1}`;
      const blockType = paragraph.getAttribute("data-block-type") || "action";
      const renderedText = paragraph.textContent?.trim() || "";
      const dialogue = blockType === "dialogue" ? renderedText.match(/^([^：:]{1,24})[：:]\s*(.*)$/) : null;
      return {
        line_id: lineId,
        block_type: blockType,
        ...(dialogue ? { speaker: dialogue[1].trim(), text: dialogue[2].trim() } : { text: renderedText }),
      };
    }).filter((block) => block.text);
    return { ...existingScene, scene_id: sceneId, heading, blocks };
  });
}

function scriptTextFromScenes(title: string, scenes: Array<Record<string, unknown>>) {
  const lines = [title];
  for (const scene of scenes) {
    const heading = readString(scene.heading || scene.title);
    if (heading) lines.push(heading);
    for (const value of readArray(scene.blocks)) {
      const block = readObject(value);
      const text = readString(block.text);
      if (!text) continue;
      const speaker = readString(block.speaker);
      lines.push(speaker ? `${speaker}: ${text}` : text);
    }
  }
  return lines.join("\n");
}

function inferBlockType(text: string): ScriptBlockType {
  if (/^(转场|闪回|切至|回到|字幕|画面)/.test(text)) return "transition";
  if (/^[△【（]/.test(text)) return "scene_note";
  if (/^[^：:]{1,12}[：:]/.test(text)) return "dialogue";
  return "action";
}

function sceneToDraft(value: unknown, index: number): SceneDraft {
  const scene = readObject(value);
  const sceneId = readString(scene.scene_id) || `scene-${index + 1}`;
  const blocks = readArray(scene.lines).length ? readArray(scene.lines) : readArray(scene.blocks).length ? readArray(scene.blocks) : readArray(scene.beats);
  return {
    sceneId,
    heading: readString(scene.heading || scene.title) || `场景 ${index + 1}`,
    lines: blocks.map((block, blockIndex) => blockToLine(block, sceneId, blockIndex)).filter((line) => line.text),
  };
}

function blockToLine(value: unknown, sceneId: string, index: number): LineDraft {
  if (typeof value === "string") {
    return { lineId: `${sceneId}-line-${index + 1}`, blockType: "action", text: value };
  }
  const block = readObject(value);
  const text = readString(block.text || block.dialogue || block.action || block.content || block.body || value);
  const blockType = (readString(block.block_type) || "action") as ScriptBlockType;
  const speaker = readString(block.speaker);
  return {
    lineId: readString(block.line_id) || `${sceneId}-line-${index + 1}`,
    blockType,
    text,
    speaker: speaker || undefined,
  };
}

function episodeToHTML(episode: EpisodeDraft) {
  const parts = [`<h2>${escapeHTML(episode.title || `第 ${episode.episodeId} 集`)}</h2>`];
  for (const scene of episode.scenes) {
    parts.push(`<section class="script-scene-block" data-scene-id="${escapeHTML(scene.sceneId)}">`);
    parts.push(`<h3>${escapeHTML(scene.heading)}</h3>`);
    for (const line of scene.lines) {
      const speaker = line.speaker ? ` data-speaker="${escapeHTML(line.speaker)}"` : "";
      parts.push(`<p data-line-id="${escapeHTML(line.lineId)}" data-block-type="${escapeHTML(line.blockType)}"${speaker}>${escapeHTML(line.text)}</p>`);
    }
    parts.push("</section>");
  }
  return parts.join("");
}

function compareEpisode(a: EpisodeDraft, b: EpisodeDraft) {
  return Number(a.episodeId) - Number(b.episodeId);
}

function readObject(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : {};
}

function readArray(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function readString(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return JSON.stringify(value);
}

function escapeHTML(value: string) {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}
