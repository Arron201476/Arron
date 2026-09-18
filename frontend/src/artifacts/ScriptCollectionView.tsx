import { useEffect, useMemo, useState } from "react";
import { AlertCircle, FileText } from "lucide-react";
import { api } from "../api";
import type { Artifact, ArtifactVersion } from "../types";

type Props = {
  label: string;
  memberArtifactType: string;
  artifacts: Artifact[];
  version: ArtifactVersion;
};

type CollectionItem = {
  artifact: Artifact;
  version: ArtifactVersion;
  episodeNo: number;
  title: string;
  summary: string;
  script: string;
};

export function ScriptCollectionView({ label, memberArtifactType, artifacts, version: aggregate }: Props) {
  const [items, setItems] = useState<CollectionItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [failure, setFailure] = useState("");
  const members = useMemo(
    () => artifacts.filter((item) => item.artifact_type === memberArtifactType && item.current_version_id).sort(compareEpisode),
    [artifacts, memberArtifactType],
  );

  useEffect(() => {
    let active = true;
    setLoading(true);
    setFailure("");
    const aggregatePayload = asRecord(aggregate.payload);
    const refs = Array.isArray(aggregatePayload.unit_refs) ? aggregatePayload.unit_refs.map(asRecord) : [];
    const count = aggregatePayload.episode_count;
    const episodeNumbers = refs.map((item) => item.episode_no);
    const versionIDs = refs.map((item) => item.artifact_version_id);
    if (!refs.length || !Number.isSafeInteger(count) || Number(count) < refs.length || (aggregatePayload.completeness === "complete" && count !== refs.length) || episodeNumbers.some((no) => !Number.isSafeInteger(no) || Number(no) < 1) || new Set(episodeNumbers).size !== refs.length || versionIDs.some((id) => typeof id !== "string" || !id) || new Set(versionIDs).size !== refs.length) {
      setItems([]); setFailure("合集版本引用不完整或重复，无法核对正文。"); setLoading(false);
      return () => { active = false; };
    }
    Promise.all(refs.map(async (ref) => {
      const version = await api.getArtifactVersion(String(ref.artifact_version_id));
      const artifact = members.find((item) => item.artifact_id === version.artifact_id);
      const payload = asRecord(version.payload);
      const episodeNo = Number(ref.episode_no);
      if (!artifact || version.artifact_version_id !== ref.artifact_version_id || !["confirmed", "superseded"].includes(version.status) || numberValue(payload.episode_no) !== episodeNo || (scopeNumber(artifact.scope_key) > 0 && scopeNumber(artifact.scope_key) !== episodeNo)) throw new Error("Collection version mismatch");
      return {
        artifact,
        version,
        episodeNo,
        title: stringValue(payload.title),
        summary: stringValue(payload.plot_summary),
        script: stringValue(payload.script_text) || stringValue(payload.text),
      };
    }))
      .then((loaded) => { if (active) setItems(loaded.sort((left, right) => left.episodeNo - right.episodeNo)); })
      .catch(() => { if (active) setFailure("单集剧本读取失败或版本引用不一致，请刷新核对。"); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [members, aggregate]);

  if (loading) return <div className="collection-state"><span className="loading-mark" /><p>正在整理全部单集剧本…</p></div>;
  if (failure) return <div className="collection-state error" role="alert"><AlertCircle size={22} /><p>{failure}</p></div>;
  if (!items.length) return <div className="collection-state"><FileText size={22} /><p>尚未生成可阅读的单集剧本。</p></div>;

  return <div className="script-collection">
    <header className="script-collection-summary"><div><strong>{label}</strong><span>{items.length} 集</span></div><p>以下内容按集数顺序合并展示，单集编辑请从左侧分集目录进入。</p></header>
    <nav className="script-collection-index" aria-label="剧本合集目录">{items.map((item) => <a key={item.artifact.artifact_id} href={`#episode-${item.episodeNo}`}>第 {item.episodeNo} 集</a>)}</nav>
    <div className="script-collection-content">{items.map((item) => <section id={`episode-${item.episodeNo}`} key={item.artifact.artifact_id} className="script-collection-episode"><header><span>第 {item.episodeNo} 集</span>{item.title && <strong>{item.title}</strong>}<small>版本 {item.version.version}</small></header>{item.summary && <div className="script-collection-plot"><strong>剧情概要</strong><p>{item.summary}</p></div>}<pre>{item.script || "本集暂无剧本正文。"}</pre></section>)}</div>
  </div>;
}

function compareEpisode(left: Artifact, right: Artifact) { return scopeNumber(left.scope_key) - scopeNumber(right.scope_key); }
function scopeNumber(scope?: string) { const match = scope?.match(/\d+/); return match ? Number(match[0]) : 0; }
function asRecord(value: unknown): Record<string, unknown> { return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {}; }
function stringValue(value: unknown) { return typeof value === "string" ? value : ""; }
function numberValue(value: unknown) { return typeof value === "number" ? value : Number(value) || 0; }
