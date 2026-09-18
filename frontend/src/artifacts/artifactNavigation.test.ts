import { describe, expect, it } from "vitest";
import { groupArtifactsForRail } from "./artifactNavigation";
import type { Artifact, ArtifactPresentation } from "../types";

const presentations: ArtifactPresentation[] = [
  {
    artifact_type: "generic_document",
    label: "文档",
    description: "通用文档",
    renderer: "document",
    editable: true,
    preferred_fields: [],
    navigation: { order: 100, group_mode: "single", visibility: "user" },
    available_actions: [],
  },
  {
    artifact_type: "episode_script",
    label: "单集剧本",
    description: "分集剧本",
    renderer: "script",
    editable: true,
    preferred_fields: [],
    navigation: { order: 200, group_mode: "episode_directory", visibility: "user" },
    available_actions: [],
  },
];

function artifact(overrides: Partial<Artifact>): Artifact {
  return {
    artifact_id: "artifact-1",
    artifact_type: "generic_document",
    current_version_id: "version-1",
    updated_at: "2026-08-24T12:00:00Z",
    ...overrides,
  };
}

describe("groupArtifactsForRail", () => {
  it("keeps independent single documents visible with their generated names", () => {
    const groups = groupArtifactsForRail(presentations, [
      artifact({ artifact_id: "outline", title: "故事大纲" }),
      artifact({ artifact_id: "episode-1", title: "第一集" }),
    ]);

    expect(groups.map((group) => group.label)).toEqual(["故事大纲", "第一集"]);
    expect(groups.map((group) => group.artifacts.length)).toEqual([1, 1]);
  });

  it("keeps episode artifacts in one directory", () => {
    const groups = groupArtifactsForRail(presentations, [
      artifact({ artifact_id: "episode-1", artifact_type: "episode_script", scope_key: "episode:1" }),
      artifact({ artifact_id: "episode-2", artifact_type: "episode_script", scope_key: "episode:2" }),
    ]);

    expect(groups).toHaveLength(1);
    expect(groups[0].label).toBe("单集剧本");
    expect(groups[0].artifacts).toHaveLength(2);
  });

  it("hides structured run state even before the registry metadata is available", () => {
    const groups = groupArtifactsForRail([], [
      artifact({ artifact_id: "state", artifact_type: "structured_run_state", title: "structured run state" }),
      artifact({ artifact_id: "document", title: "用户文档" }),
    ]);

    expect(groups.map((group) => group.label)).toEqual(["用户文档"]);
  });

  it("keeps structured run state internal when stale metadata marks it as user visible", () => {
    const stalePresentation: ArtifactPresentation = {
      artifact_type: "structured_run_state",
      label: "structured run state",
      description: "stale metadata",
      renderer: "document",
      editable: true,
      preferred_fields: [],
      navigation: { order: 1, group_mode: "single", visibility: "user" },
      available_actions: ["manual_edit"],
    };

    expect(groupArtifactsForRail([stalePresentation], [
      artifact({ artifact_id: "state", artifact_type: "structured_run_state" }),
    ])).toEqual([]);
  });
});
