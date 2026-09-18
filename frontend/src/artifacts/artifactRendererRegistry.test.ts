import { describe, expect, it } from "vitest";
import { artifactUsesDirectEditor, rendererForArtifact } from "./artifactRendererRegistry";
import type { ArtifactPresentation } from "../types";

describe("artifact renderer registry", () => {
  it("keeps renderer selection in the Agent shell", () => {
    const video = presentation("video_script");
    const script = presentation("script");
    const document = presentation("document");
    expect(rendererForArtifact(video).kind).toBe("video_script");
    expect(rendererForArtifact(script).kind).toBe("script");
    expect(rendererForArtifact(document).kind).toBe("document");
    expect(artifactUsesDirectEditor(video)).toBe(true);
  });
});

function presentation(renderer: ArtifactPresentation["renderer"]): ArtifactPresentation {
  return { artifact_type: "test", label: "测试", description: "测试", renderer, editable: true, preferred_fields: [], navigation: { order: 1, group_mode: "single", visibility: "user" }, available_actions: [] };
}
