// @vitest-environment jsdom
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { Artifact, ArtifactVersion } from "../types";
import { ScriptCollectionView } from "./ScriptCollectionView";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("ScriptCollectionView", () => {
  it("loads and presents the actual episode scripts in order", async () => {
    const artifacts: Artifact[] = [episode("a2", 2, "v2"), episode("a1", 1, "v1")];
    vi.spyOn(api, "getArtifactVersion").mockImplementation(async (versionID) => ({
      artifact_version_id: versionID,
      artifact_id: versionID === "v1" ? "a1" : "a2",
      version: 1,
      status: "confirmed",
      creation_reason: "generated",
      created_at: "2026-08-11T00:00:00Z",
      payload: { episode_no: versionID === "v1" ? 1 : 2, script_text: versionID === "v1" ? "第一集正文" : "第二集正文" },
    }));

    render(<ScriptCollectionView label="完整剧本" memberArtifactType="script_unit" artifacts={artifacts} version={aggregate(["v1", "v2"])} />);
    await waitFor(() => expect(screen.getByText("第一集正文")).toBeTruthy());
    expect(screen.getByText("第二集正文")).toBeTruthy();
    const episodes = document.querySelectorAll(".script-collection-episode");
    expect(episodes[0].id).toBe("episode-1");
    expect(episodes[1].id).toBe("episode-2");
  });
});

function aggregate(ids: string[]): ArtifactVersion {
  return { artifact_id: "collection", artifact_version_id: "collection-v1", version: 1, status: "confirmed", creation_reason: "aggregate", created_at: "2026-09-09T00:00:00Z", payload: { episode_count: ids.length, completeness: "complete", unit_refs: ids.map((id, index) => ({ episode_no: index + 1, artifact_version_id: id })) } };
}

it("uses the saved collection references rather than the current member versions", async () => {
  vi.spyOn(api, "getArtifactVersion").mockResolvedValue({ ...aggregate([]), artifact_id: "a1", artifact_version_id: "historical-unit", status: "superseded", payload: { episode_no: 1, script_text: "Historical episode body" } });
  render(<ScriptCollectionView label="历史候选稿" memberArtifactType="script_unit" artifacts={[episode("a1", 1, "current-unit")]} version={aggregate(["historical-unit"])} />);
  await screen.findByText("Historical episode body");
  expect(api.getArtifactVersion).toHaveBeenCalledWith("historical-unit");
  expect(api.getArtifactVersion).not.toHaveBeenCalledWith("current-unit");
});

it.each(["foreign_artifact", "wrong_version", "draft", "wrong_episode"])("rejects collection unit %s instead of showing unrelated or unconfirmed text", async (scenario) => {
  const version = { ...aggregate([]), artifact_id: "a1", artifact_version_id: "unit", payload: { episode_no: 1, script_text: "Wrong body" } };
  if (scenario === "foreign_artifact") version.artifact_id = "foreign";
  if (scenario === "wrong_version") version.artifact_version_id = "other-unit";
  if (scenario === "draft") version.status = "draft";
  if (scenario === "wrong_episode") version.payload.episode_no = 2;
  vi.spyOn(api, "getArtifactVersion").mockResolvedValue(version);
  render(<ScriptCollectionView label="Collection" memberArtifactType="script_unit" artifacts={[episode("a1", 1, "unit")]} version={aggregate(["unit"])} />);
  await screen.findByRole("alert");
  expect(screen.queryByText("Wrong body")).toBeNull();
});

it("does not silently replace missing or duplicate saved references with current members", async () => {
  const fetcher = vi.spyOn(api, "getArtifactVersion");
  const view = render(<ScriptCollectionView label="Collection" memberArtifactType="script_unit" artifacts={[episode("a1", 1, "current")]} version={aggregate([])} />);
  expect(screen.getByRole("alert")).toBeTruthy();
  view.rerender(<ScriptCollectionView label="Collection" memberArtifactType="script_unit" artifacts={[episode("a1", 1, "current")]} version={aggregate(["repeated", "repeated"])} />);
  expect(screen.getByRole("alert")).toBeTruthy();
  expect(fetcher).not.toHaveBeenCalled();
});

it("discards a late unit response when a different collection is selected", async () => {
  let finish!: (value: ArtifactVersion) => void;
  vi.spyOn(api, "getArtifactVersion").mockImplementationOnce(() => new Promise((done) => { finish = done; })).mockResolvedValue({ ...aggregate([]), artifact_id: "a1", artifact_version_id: "new", payload: { episode_no: 1, script_text: "New body" } });
  const artifacts = [episode("a1", 1, "new")];
  const view = render(<ScriptCollectionView label="Collection" memberArtifactType="script_unit" artifacts={artifacts} version={aggregate(["old"])} />);
  view.rerender(<ScriptCollectionView label="Collection" memberArtifactType="script_unit" artifacts={artifacts} version={{ ...aggregate(["new"]), artifact_version_id: "collection-v2" }} />);
  await screen.findByText("New body");
  await act(async () => finish({ ...aggregate([]), artifact_id: "a1", artifact_version_id: "old", payload: { episode_no: 1, script_text: "Late old body" } }));
  expect(screen.queryByText("Late old body")).toBeNull();
});

function episode(id: string, no: number, versionID: string): Artifact {
  return { artifact_id: id, run_id: "run-1", artifact_type: "script_unit", scope_key: `episode:${no}`, current_version_id: versionID, updated_at: "2026-08-11T00:00:00Z" };
}
