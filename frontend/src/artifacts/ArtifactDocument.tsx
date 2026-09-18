import { useEffect, useRef, useState, type ReactNode } from "react";
import { ArrowDown, ArrowUp, ChevronDown, ChevronLeft, ChevronRight, ListTree, Plus, Search, Trash2 } from "lucide-react";
import { fieldLabel, isTechnicalField, orderedEntries, scalarLabel } from "./artifactPresentation";
import { captureDOMSelection, type SelectionCaptureBase } from "../contextTargeting";
import type { AgentTargetSelection, ArtifactPresentation } from "../types";

type Props = {
  artifactType: string;
  presentation: ArtifactPresentation;
  payload: unknown;
  editing?: boolean;
  onChange?: (payload: unknown) => void;
  selectionBase?: SelectionCaptureBase;
  onSelectionChange?: (selection: AgentTargetSelection | null) => void;
};

export function ArtifactDocument({ artifactType, presentation: config, payload, editing = false, onChange, selectionBase, onSelectionChange }: Props) {
  const [query, setQuery] = useState("");
  const documentRef = useRef<HTMLDivElement>(null);
  const normalizedQuery = query.trim().toLowerCase();
  const captureSelection = () => {
    if (editing || !selectionBase || !documentRef.current) return;
    onSelectionChange?.(captureDOMSelection(documentRef.current, selectionBase));
  };

  if (typeof payload === "string") {
    return editing ? <textarea className="artifact-long-text-editor" value={payload} onChange={(event) => onChange?.(event.currentTarget.value)} /> : <div ref={documentRef} className="artifact-prose" data-field-path="content" onMouseUp={captureSelection} onKeyUp={captureSelection}>{payload}</div>;
  }
  if (!isRecord(payload)) return <div className="artifact-empty">暂无可展示内容</div>;

  const entries = orderedEntries(payload, config.preferred_fields);
  const contentEntries = entries.filter(([key]) => !isTechnicalField(key));
  const visibleEntries = contentEntries.filter(([key, value]) => !normalizedQuery || `${fieldLabel(key)} ${searchText(value)}`.toLowerCase().includes(normalizedQuery));
  const directScript = typeof payload.script_text === "string" ? payload.script_text : null;

  if (!editing && directScript && ["script", "video_script"].includes(config.renderer)) {
    return <div ref={documentRef} onMouseUp={captureSelection} onKeyUp={captureSelection}><ScriptDocument artifactType={artifactType} payload={payload} script={directScript} /></div>;
  }

  if (config.renderer === "episode_plan" && Array.isArray(payload.episodes)) {
    return (
      <div ref={documentRef} className={`artifact-document episode-plan-document ${editing ? "editing" : ""}`} onMouseUp={captureSelection} onKeyUp={captureSelection}>
        <div className="artifact-document-tools">
          <div><ListTree size={15} /><span>{config.description}</span></div>
          {editing ? <span className="artifact-edit-guidance">逐集修改规划内容，保存时统一形成新版本</span> : null}
        </div>
        <EpisodePlanDirectory
          payload={payload}
		  preferredFields={config.preferred_fields}
          editing={editing}
          onChange={(next) => onChange?.(next)}
        />
      </div>
    );
  }

  return (
    <div ref={documentRef} className={`artifact-document ${editing ? "editing" : ""}`} onMouseUp={captureSelection} onKeyUp={captureSelection}>
      <div className="artifact-document-tools">
        <div><ListTree size={15} /><span>{config.description}</span></div>
        {editing ? <span className="artifact-edit-guidance">逐项修改业务内容，来源引用由系统自动维护</span> : <label className="artifact-document-search"><Search size={14} /><input aria-label="搜索产物内容" value={query} onChange={(event) => setQuery(event.currentTarget.value)} placeholder="搜索当前产物" /></label>}
      </div>
      <div className="artifact-sections">
        {visibleEntries.map(([key, value], index) => (
          <ArtifactSection key={key} fieldKey={key} open={index < 4}>
            {editing ? <EditableValue fieldKey={key} value={value} onChange={(next) => onChange?.({ ...payload, [key]: next })} /> : <ReadableValue fieldKey={key} fieldPath={key} value={value} />}
          </ArtifactSection>
        ))}
        {!visibleEntries.length && <div className="artifact-empty">没有匹配的内容</div>}
      </div>
    </div>
  );
}

const episodeDirectoryPageSize = 12;

function EpisodePlanDirectory({ payload, preferredFields, editing, onChange }: { payload: Record<string, unknown>; preferredFields: string[]; editing: boolean; onChange: (payload: unknown) => void }) {
  const episodes = payload.episodes as unknown[];
  const [selectedIndex, setSelectedIndex] = useState(0);
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);
  const normalizedQuery = query.trim().toLowerCase();
  const matchingIndexes = episodes.map((episode, index) => ({ episode, index })).filter(({ episode, index }) => {
    if (!normalizedQuery) return true;
    return `${episodeTitle(episode, index)} ${searchText(episode)}`.toLowerCase().includes(normalizedQuery);
  });
  const pageCount = Math.max(1, Math.ceil(matchingIndexes.length / episodeDirectoryPageSize));
  const safePage = Math.min(page, pageCount - 1);
  const visibleEpisodes = matchingIndexes.slice(safePage * episodeDirectoryPageSize, (safePage + 1) * episodeDirectoryPageSize);
  const selectedEpisode = episodes[selectedIndex];
  const globalEntries = orderedEntries(payload, preferredFields).filter(([key]) => key !== "episodes" && !isTechnicalField(key));

  useEffect(() => { setPage(0); }, [normalizedQuery]);
  useEffect(() => {
    if (selectedIndex >= episodes.length) setSelectedIndex(Math.max(0, episodes.length - 1));
  }, [episodes.length, selectedIndex]);

  const updateEpisode = (next: unknown) => {
    const updated = episodes.slice();
    updated[selectedIndex] = next;
    onChange({ ...payload, episodes: updated });
  };

  return <div className="episode-plan-layout">
    <nav className="episode-plan-directory" aria-label="分集规划目录">
      <header><div><strong>集数目录</strong><span>{episodes.length} 集</span></div><label><Search size={13} /><input aria-label="搜索分集规划" value={query} onChange={(event) => setQuery(event.currentTarget.value)} placeholder="搜索集数或内容" /></label></header>
      <div className="episode-plan-directory-grid">
        {visibleEpisodes.map(({ episode, index }) => <button type="button" key={itemKey(episode, index)} className={index === selectedIndex ? "selected" : ""} aria-current={index === selectedIndex ? "page" : undefined} onClick={() => setSelectedIndex(index)}><span>{episodeNumber(episode, index)}</span><strong>{episodeShortTitle(episode, index)}</strong></button>)}
        {!visibleEpisodes.length && <span className="episode-plan-no-results">没有匹配的集数</span>}
      </div>
      {pageCount > 1 && <footer><button type="button" aria-label="上一页" title="上一页" disabled={safePage === 0} onClick={() => setPage((current) => Math.max(0, current - 1))}><ChevronLeft size={15} /></button><span>{safePage + 1} / {pageCount}</span><button type="button" aria-label="下一页" title="下一页" disabled={safePage === pageCount - 1} onClick={() => setPage((current) => Math.min(pageCount - 1, current + 1))}><ChevronRight size={15} /></button></footer>}
    </nav>
    <section className="episode-plan-detail" aria-live="polite">
      {selectedEpisode !== undefined ? <>
        <header><span>第 {episodeNumber(selectedEpisode, selectedIndex)} 集</span><strong>{episodeShortTitle(selectedEpisode, selectedIndex)}</strong></header>
        <div className="episode-plan-detail-body">
          {editing ? <EditableValue fieldKey="episodes" value={selectedEpisode} onChange={updateEpisode} /> : <ReadableValue fieldKey="episodes" fieldPath={`episodes[${selectedIndex}]`} value={selectedEpisode} />}
        </div>
      </> : <div className="artifact-empty">暂无分集规划</div>}
      {globalEntries.length > 0 && <details className="episode-plan-global"><summary>全局衔接信息<ChevronDown size={15} /></summary><div>{globalEntries.map(([key, value]) => <section key={key}><h4>{fieldLabel(key)}</h4>{editing ? <EditableValue fieldKey={key} value={value} onChange={(next) => onChange({ ...payload, [key]: next })} /> : <ReadableValue fieldKey={key} fieldPath={key} value={value} />}</section>)}</div></details>}
    </section>
  </div>;
}

function episodeNumber(value: unknown, index: number) { if (isRecord(value)) return Number(value.episode_no ?? value.episode_id) || index + 1; return index + 1; }
function episodeShortTitle(value: unknown, index: number) { if (!isRecord(value)) return `第 ${index + 1} 集`; const title = value.title ?? value.episode_title ?? value.core_event ?? value.episode_goal; return title ? String(title) : `第 ${episodeNumber(value, index)} 集`; }
function episodeTitle(value: unknown, index: number) { return `第 ${episodeNumber(value, index)} 集 ${episodeShortTitle(value, index)}`; }

function ScriptDocument({ artifactType, payload, script }: { artifactType: string; payload: Record<string, unknown>; script: string }) {
  const meta = [
    payload.episode_no != null ? `第 ${payload.episode_no} 集` : "",
    typeof payload.title === "string" ? payload.title : "",
    artifactType === "video_script_unit" && typeof payload.source_file_name === "string" ? payload.source_file_name : "",
  ].filter(Boolean);
  const lines = scriptLineMetadata(payload, script);
  return <div className="script-document"><header>{meta.map((item) => <span key={String(item)}>{String(item)}</span>)}</header>{typeof payload.plot_summary === "string" && <section className="script-summary" data-field-path="plot_summary"><h3>剧情概要</h3><p>{payload.plot_summary}</p></section>}<div className="script-text-lines" data-field-path="script_text">{lines.map((line, index) => <div key={`${line.lineID || "line"}-${index}`} data-field-path="script_text" data-episode-id={line.episodeID} data-scene-id={line.sceneID} data-line-id={line.lineID} data-block-type={line.blockType} data-location-label={line.locationLabel}>{line.text || "\u00a0"}</div>)}</div>{Array.isArray(payload.uncertainty_flags) && payload.uncertainty_flags.length > 0 && <section className="script-flags"><h3>不确定项</h3><ReadableValue value={payload.uncertainty_flags} /></section>}</div>;
}

function scriptLineMetadata(payload: Record<string, unknown>, script: string) {
  const episodeID = String(payload.episode_no ?? payload.episode_id ?? "");
  const metadata = new Map<string, Array<{ sceneID: string; lineID: string; blockType: string }>>();
  const scenes = Array.isArray(payload.scenes) ? payload.scenes : [];
  for (const sceneValue of scenes) {
    if (!isRecord(sceneValue)) continue;
    const sceneID = String(sceneValue.scene_id ?? "");
    for (const blockValue of Array.isArray(sceneValue.blocks) ? sceneValue.blocks : []) {
      if (!isRecord(blockValue)) continue;
      const text = String(blockValue.text ?? "");
      const speaker = String(blockValue.speaker ?? "");
      const candidates = [text, speaker ? `${speaker}：${text}` : "", speaker ? `${speaker}: ${text}` : ""].filter(Boolean);
      for (const candidate of candidates) {
        const values = metadata.get(normalizeLine(candidate)) ?? [];
        values.push({ sceneID, lineID: String(blockValue.line_id ?? ""), blockType: String(blockValue.block_type ?? "") });
        metadata.set(normalizeLine(candidate), values);
      }
    }
  }
  return script.split(/\r?\n/).map((text) => {
    const matches = metadata.get(normalizeLine(text));
    const match = matches?.shift();
    return {
      text,
      episodeID,
      sceneID: match?.sceneID ?? "",
      lineID: match?.lineID ?? "",
      blockType: match?.blockType ?? "",
      locationLabel: [episodeID ? `第 ${episodeID} 集` : "", match?.sceneID ? `场景 ${match.sceneID}` : "", match?.lineID ? `台词 ${match.lineID}` : ""].filter(Boolean).join(" / "),
    };
  });
}

function normalizeLine(value: string) { return value.replace(/\s+/g, " ").replace(/：/g, ":").trim(); }

function ArtifactSection({ fieldKey, open, children }: { fieldKey: string; open: boolean; children: ReactNode }) {
  return <details className="artifact-section" open={open}><summary><h3>{fieldLabel(fieldKey)}</h3><ChevronDown size={16} /></summary><div className="artifact-section-content">{children}</div></details>;
}

function ReadableValue({ value, fieldKey, fieldPath }: { value: unknown; fieldKey?: string; fieldPath?: string }): ReactNode {
  if (value === null || value === undefined || value === "") return <span className="artifact-muted">暂无</span>;
  if (["string", "number", "boolean"].includes(typeof value)) return <span className="artifact-scalar" data-field-path={fieldPath}>{scalarLabel(value)}</span>;
  if (Array.isArray(value)) {
    if (!value.length) return <span className="artifact-muted">暂无内容</span>;
    if (value.every((item) => item === null || ["string", "number", "boolean"].includes(typeof item))) return <ul className="artifact-bullets">{value.map((item, index) => <li key={index} data-field-path={`${fieldPath ?? fieldKey ?? "item"}[${index}]`}>{scalarLabel(item)}</li>)}</ul>;
    return <div className="artifact-items">{value.map((item, index) => <section className="artifact-item" key={itemKey(item, index)}><h4>{itemTitle(fieldKey, item, index)}</h4><ReadableValue value={item} fieldPath={`${fieldPath ?? fieldKey ?? "item"}[${index}]`} /></section>)}</div>;
  }
  const record = isRecord(value) ? value : {};
  const entries = Object.entries(record).filter(([key]) => !isTechnicalField(key));
  if (!entries.length) return <span className="artifact-muted">暂无内容</span>;
  return <dl className="artifact-kv">{entries.map(([key, item]) => <div key={key}><dt>{fieldLabel(key)}</dt><dd><ReadableValue fieldKey={key} fieldPath={fieldPath ? `${fieldPath}.${key}` : key} value={item} /></dd></div>)}</dl>;
}

function EditableValue({ value, fieldKey, onChange }: { value: unknown; fieldKey?: string; onChange: (value: unknown) => void }): ReactNode {
  if (typeof value === "boolean") return <label className="artifact-toggle"><input type="checkbox" checked={value} onChange={(event) => onChange(event.currentTarget.checked)} /><span>{value ? "是" : "否"}</span></label>;
  if (typeof value === "number") return <input className="artifact-field-input" type="number" value={value} onChange={(event) => onChange(event.currentTarget.value === "" ? 0 : Number(event.currentTarget.value))} />;
  if (typeof value === "string" || value == null) return <textarea className="artifact-field-textarea" value={String(value ?? "")} onChange={(event) => onChange(event.currentTarget.value)} />;
  if (Array.isArray(value)) {
    return <div className="artifact-edit-list">{value.map((item, index) => <section key={itemKey(item, index)}><header><strong>{itemTitle(fieldKey, item, index)}</strong><div><button type="button" aria-label="上移" disabled={index === 0} onClick={() => onChange(moveItem(value, index, index - 1))}><ArrowUp size={14} /></button><button type="button" aria-label="下移" disabled={index === value.length - 1} onClick={() => onChange(moveItem(value, index, index + 1))}><ArrowDown size={14} /></button><button type="button" aria-label="删除" onClick={() => onChange(value.filter((_, itemIndex) => itemIndex !== index))}><Trash2 size={14} /></button></div></header><EditableValue fieldKey={fieldKey} value={item} onChange={(next) => { const copy = value.slice(); copy[index] = next; onChange(copy); }} /></section>)}<button className="artifact-add-item" type="button" onClick={() => onChange([...value, blankLike(value[0])])}><Plus size={14} />新增一项</button></div>;
  }
  const record = isRecord(value) ? value : {};
  const businessEntries = Object.entries(record).filter(([key]) => !isTechnicalField(key));
  if (!businessEntries.length) return <span className="artifact-muted">系统引用随内容自动维护</span>;
  return <div className="artifact-edit-object">{businessEntries.map(([key, item]) => <label key={key}><span>{fieldLabel(key)}</span><EditableValue fieldKey={key} value={item} onChange={(next) => onChange({ ...record, [key]: next })} /></label>)}</div>;
}

function moveItem(items: unknown[], from: number, to: number) { const next = items.slice(); const [item] = next.splice(from, 1); next.splice(to, 0, item); return next; }
function blankLike(value: unknown): unknown { if (typeof value === "number") return 0; if (typeof value === "boolean") return false; if (typeof value === "string" || value == null) return ""; if (Array.isArray(value)) return []; if (isRecord(value)) return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, key.endsWith("_no") ? 0 : blankLike(item)])); return ""; }
function itemKey(value: unknown, index: number) { if (!isRecord(value)) return `item-${index}`; return String(value.episode_id ?? value.episode_no ?? value.scene_id ?? value.option_id ?? value.id ?? value.name ?? `item-${index}`); }
function itemTitle(fieldKey: string | undefined, value: unknown, index: number) { if (isRecord(value)) { const title = value.title ?? value.name ?? value.label ?? value.episode_title ?? value.scene_heading; if (title) return String(title); const episode = value.episode_no ?? value.episode_id; if (episode != null) return `第 ${episode} 集`; } return `${fieldLabel(fieldKey ?? "item")} ${index + 1}`; }
function isRecord(value: unknown): value is Record<string, unknown> { return Boolean(value) && typeof value === "object" && !Array.isArray(value); }
function searchText(value: unknown) { try { return JSON.stringify(value); } catch { return String(value); } }
