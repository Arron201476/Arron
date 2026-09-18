import type { ArtifactPresentation } from "../types";

export type ArtifactRendererKind = ArtifactPresentation["renderer"];

export function rendererForArtifact(presentation: ArtifactPresentation): { kind: ArtifactRendererKind } {
  return { kind: presentation.renderer };
}

export function artifactUsesDirectEditor(presentation: ArtifactPresentation): boolean {
  const kind = presentation.renderer;
  return kind === "script" || kind === "video_script";
}
