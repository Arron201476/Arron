import { CircleAlert, Film, Play, ScanText } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import type { SelectionCaptureBase } from "../contextTargeting";
import type { AgentTargetSelection, Asset } from "../types";
import { ScriptArtifactEditor } from "./ScriptArtifactEditor";

type Props = {
  payload: unknown;
  projectAssets?: Asset[];
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
};

type SceneEntry = {
  id: string;
  heading: string;
  startMS: number;
  endMS: number;
};

export function VideoScriptArtifactEditor(props: Props) {
  const payload = asRecord(props.payload);
  const videoRef = useRef<HTMLVideoElement>(null);
  const editorPaneRef = useRef<HTMLDivElement>(null);
  const [activeSceneID, setActiveSceneID] = useState("");
  const [useCompatiblePreview, setUseCompatiblePreview] = useState(false);
  const [previewReady, setPreviewReady] = useState(false);
  const [previewFailed, setPreviewFailed] = useState(false);
  const scenes = useMemo(() => sceneEntries(payload), [payload]);
  const assetID = sourceAssetID(payload, props.projectAssets ?? []);
  const summary = typeof payload.plot_summary === "string" ? payload.plot_summary : "";
  const sourceName = typeof payload.source_file_name === "string" ? payload.source_file_name : "原始视频";
  const completeness = asRecord(payload.extraction_completeness);
  const needsReview = completeness.status === "needs_review" || (Array.isArray(payload.uncertainty_flags) && payload.uncertainty_flags.length > 0);

  useEffect(() => {
    setUseCompatiblePreview(false);
    setPreviewReady(false);
    setPreviewFailed(false);
  }, [assetID]);

  const videoSource = assetID
    ? `/api/v1/assets/${encodeURIComponent(assetID)}/content${useCompatiblePreview ? "?browser_compatible=1" : ""}`
    : "";

  const handleVideoError = () => {
    if (!useCompatiblePreview) {
      setUseCompatiblePreview(true);
      setPreviewReady(false);
      return;
    }
    setPreviewFailed(true);
  };

  const seekTo = (scene: SceneEntry) => {
    setActiveSceneID(scene.id);
    seekVideo(videoRef.current, scene.startMS);
    const headings = editorPaneRef.current?.querySelectorAll<HTMLElement>(".script-scene-heading") ?? [];
    const target = Array.from(headings).find((heading) => normalizedHeading(heading.textContent ?? "").startsWith(normalizedHeading(scene.heading)));
    headings.forEach((heading) => heading.classList.toggle("active-scene", heading === target));
    target?.scrollIntoView({ behavior: "smooth", block: "start" });
  };

  return <div className="video-script-workspace">
    <aside className="video-reference-pane" aria-label="原视频与场次导航">
      <div className="video-reference-heading"><Film size={16} /><span><strong>{sourceName}</strong><small>原视频对照</small></span></div>
      {assetID ? <div className="video-reference-player">
        <video key={videoSource} ref={videoRef} controls preload="metadata" src={videoSource} onError={handleVideoError} onCanPlay={() => { setPreviewReady(true); setPreviewFailed(false); }} />
        {useCompatiblePreview && !previewReady && !previewFailed && <div className="video-preview-status">正在生成兼容预览...</div>}
        {previewFailed && <div className="video-preview-status error"><CircleAlert size={16} /><span>视频预览生成失败</span></div>}
      </div> : <div className="video-reference-missing"><CircleAlert size={18} /><span>未找到原视频引用</span></div>}
      <section className="video-summary-panel">
        <header><ScanText size={14} /><strong>剧情概要</strong>{needsReview && <span>需复核</span>}</header>
        <p>{summary || "暂未生成剧情概要。"}</p>
      </section>
    </aside>
    <div ref={editorPaneRef} className="video-script-editor-pane">
      {scenes.length > 0 && <nav className="video-scene-index video-scene-strip" aria-label="视频场次">
        <header><strong>场次定位</strong><span>{scenes.length} 场</span></header>
        <div className="video-scene-strip-list">
          {scenes.map((scene, index) => <button type="button" key={scene.id} className={activeSceneID === scene.id ? "active" : ""} onClick={() => seekTo(scene)} aria-label={`${scene.heading || `场 ${index + 1}`} ${formatTime(scene.startMS)} 至 ${formatTime(scene.endMS)}`}>
            <Play size={11} fill="currentColor" /><span><strong>{scene.heading || `场 ${index + 1}`}</strong><small>{formatTime(scene.startMS)} - {formatTime(scene.endMS)}</small></span>
          </button>)}
        </div>
      </nav>}
      <ScriptArtifactEditor {...props} highlightSpeakers />
    </div>
  </div>;
}

function seekVideo(video: HTMLVideoElement | null, startMS: number) {
  if (!video) return;
  const apply = () => {
    video.currentTime = Math.max(0, startMS / 1000);
    void video.play().catch(() => undefined);
  };
  if (video.readyState === HTMLMediaElement.HAVE_NOTHING) video.addEventListener("loadedmetadata", apply, { once: true });
  else apply();
}

function normalizedHeading(value: string) {
  return value.replace(/\s+/g, "").replace(/－/g, "-").trim();
}

function sceneEntries(payload: Record<string, unknown>): SceneEntry[] {
  if (!Array.isArray(payload.scenes)) return [];
  return payload.scenes.flatMap((value, index) => {
    const scene = asRecord(value);
    const range = sceneTimeRange(scene);
    return [{
      id: String(scene.scene_id ?? `scene-${index + 1}`),
      heading: String(scene.heading ?? `场 ${index + 1}`),
      startMS: numberValue(range.start_ms),
      endMS: numberValue(range.end_ms),
    }];
  });
}

function sceneTimeRange(scene: Record<string, unknown>): Record<string, unknown> {
  const direct = asRecord(scene.time_range);
  if (typeof direct.start_ms === "number" || typeof direct.end_ms === "number") return direct;
  if (!Array.isArray(scene.source_refs)) return {};
  for (const value of scene.source_refs) {
    const sourceRef = asRecord(value);
    if (sourceRef.source_type !== "video_time_range") continue;
    const range = asRecord(sourceRef.time_range);
    if (typeof range.start_ms === "number" || typeof range.end_ms === "number") return range;
  }
  return {};
}

function sourceAssetID(payload: Record<string, unknown>, projectAssets: Asset[]): string {
  const topLevel = firstAssetID(payload.source_refs);
  if (topLevel) return topLevel;
  if (Array.isArray(payload.scenes)) {
    for (const scene of payload.scenes) {
      const found = firstAssetID(asRecord(scene).source_refs);
      if (found) return found;
    }
  }
  const sourceName = typeof payload.source_file_name === "string" ? payload.source_file_name.trim() : "";
  if (!sourceName) return "";
  const matches = projectAssets.filter((asset) =>
    asset.kind === "video" &&
    asset.status === "available" &&
    (asset.display_name === sourceName || asset.original_filename === sourceName),
  );
  return matches.length === 1 ? matches[0].asset_id : "";
}

function firstAssetID(value: unknown): string {
  if (!Array.isArray(value)) return "";
  for (const item of value) {
    const id = asRecord(item).asset_id;
    if (typeof id === "string" && id) return id;
  }
  return "";
}

function formatTime(milliseconds: number) {
  const seconds = Math.max(0, Math.floor(milliseconds / 1000));
  return `${String(Math.floor(seconds / 60)).padStart(2, "0")}:${String(seconds % 60).padStart(2, "0")}`;
}

function numberValue(value: unknown) { return typeof value === "number" && Number.isFinite(value) ? value : 0; }
function asRecord(value: unknown): Record<string, unknown> { return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {}; }
