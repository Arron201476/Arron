import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "./api";
import { renameProjectWithCurrentVersion } from "./App";
import type { Project } from "./types";

const project = (version: number, title = "旧名称") => ({
  project_id: "prj_rename",
  title,
  version,
} as Project);

describe("renameProjectWithCurrentVersion", () => {
  afterEach(() => vi.restoreAllMocks());

  it.each([1, 2])("stops writing when the request scope changes during version read %s", async (expiredRead) => {
    let active = true;
    let reads = 0;
    const get = vi.spyOn(api, "getProject").mockImplementation(async () => {
      ++reads;
      if (reads === expiredRead) active = false;
      return project(reads);
    });
    const rename = vi.spyOn(api, "renameProject").mockRejectedValue(new ApiError("PROJECT_VERSION_CONFLICT", "stale", 409));
    await expect(renameProjectWithCurrentVersion("prj_rename", "New title", () => active)).rejects.toMatchObject({ code: "ROLE_FORBIDDEN" });
    expect(get).toHaveBeenCalledTimes(expiredRead);
    expect(rename).toHaveBeenCalledTimes(expiredRead - 1);
  });

  it("does not read an old target after its request scope is invalidated", async () => {
    const get = vi.spyOn(api, "getProject");
    const rename = vi.spyOn(api, "renameProject");
    await expect(renameProjectWithCurrentVersion("prj_rename", "New title", () => false)).rejects.toMatchObject({ code: "ROLE_FORBIDDEN" });
    expect(get).not.toHaveBeenCalled();
    expect(rename).not.toHaveBeenCalled();
  });

  it("does not start conflict recovery after a role or account change", async () => {
    let active = true;
    const get = vi.spyOn(api, "getProject").mockResolvedValue(project(1));
    const rename = vi.spyOn(api, "renameProject").mockImplementation(async () => {
      active = false;
      throw new ApiError("PROJECT_VERSION_CONFLICT", "stale", 409);
    });
    await expect(renameProjectWithCurrentVersion("prj_rename", "New title", () => active)).rejects.toMatchObject({ code: "ROLE_FORBIDDEN" });
    expect(get).toHaveBeenCalledOnce();
    expect(rename).toHaveBeenCalledOnce();
  });

  it("refreshes a stale running-project version and retries once", async () => {
    vi.spyOn(api, "getProject")
      .mockResolvedValueOnce(project(4))
      .mockResolvedValueOnce(project(5));
    vi.spyOn(api, "renameProject")
      .mockRejectedValueOnce(new ApiError("PROJECT_VERSION_CONFLICT", "stale", 409))
      .mockResolvedValueOnce(project(6, "新名称"));

    const updated = await renameProjectWithCurrentVersion("prj_rename", "新名称");

    expect(updated.title).toBe("新名称");
    expect(api.getProject).toHaveBeenCalledTimes(2);
    expect(api.renameProject).toHaveBeenNthCalledWith(2, expect.objectContaining({ version: 5 }), "新名称");
  });
});
