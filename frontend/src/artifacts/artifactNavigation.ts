import { presentationFor } from "./artifactPresentation";
import { episodeNumberFromScope } from "../contextTargeting";
import type { Artifact, ArtifactPresentation } from "../types";

export type ArtifactRailGroup = {
  key: string;
  type: string;
  label: string;
  presentation: ArtifactPresentation;
  artifacts: Artifact[];
};

export function groupArtifactsForRail(presentations: ArtifactPresentation[], artifacts: Artifact[]): ArtifactRailGroup[] {
  const groups = new Map<string, Artifact[]>();
  const visible = artifacts
    .filter((artifact) => artifactIsVisible(presentations, artifact))
    .sort((left, right) => compareArtifacts(presentations, left, right));

  for (const artifact of visible) {
    const presentation = presentationFor(presentations, artifact.artifact_type);
    const key = presentation.navigation.group_mode === "episode_directory"
      ? artifact.artifact_type
      : `${artifact.artifact_type}:${artifact.artifact_id}`;
    groups.set(key, [...(groups.get(key) ?? []), artifact]);
  }

  return [...groups].map(([key, items]) => {
    const type = items[0].artifact_type;
    const presentation = presentationFor(presentations, type);
    const label = presentation.navigation.group_mode === "single"
      ? items[0].title?.trim() || presentation.label
      : presentation.label;
    return { key, type, label, presentation, artifacts: items };
  });
}

export function compareArtifacts(presentations: ArtifactPresentation[], left: Artifact, right: Artifact): number {
  const stage = presentationFor(presentations, left.artifact_type).navigation.order
    - presentationFor(presentations, right.artifact_type).navigation.order;
  if (stage) return stage;
  const scope = scopeNumber(left.scope_key) - scopeNumber(right.scope_key);
  return scope || left.updated_at.localeCompare(right.updated_at);
}

export function artifactIsVisible(presentations: ArtifactPresentation[], artifact: Artifact): boolean {
  return presentationFor(presentations, artifact.artifact_type).navigation.visibility === "user";
}

function scopeNumber(scope?: string | null): number {
  return episodeNumberFromScope(scope) ?? 0;
}
