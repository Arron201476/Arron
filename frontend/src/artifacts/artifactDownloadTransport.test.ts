import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";

afterEach(() => { vi.unstubAllGlobals(); });

describe("artifact download transport", () => {
  it("encodes the exact version and format and returns file bytes", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response("exact file", { status: 200 }));
    vi.stubGlobal("fetch", fetcher);
    const controller = new AbortController();
    const blob = await api.downloadArtifactVersion("av/one", "txt & data", controller.signal);
    expect(await blob.text()).toBe("exact file");
    expect(fetcher).toHaveBeenCalledWith("/api/v1/artifact-versions/av%2Fone/download?format=txt+%26+data", { signal: controller.signal });
  });
  it("propagates permission errors instead of returning an error file", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: "FORBIDDEN", message: "Permission denied" } }), { status: 403 })));
    await expect(api.downloadArtifactVersion("av", "txt")).rejects.toMatchObject({ code: "FORBIDDEN", message: "Permission denied", status: 403 });
  });
  it("reports non-JSON failures without saving them", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("upstream failed", { status: 502 })));
    await expect(api.downloadArtifactVersion("av", "txt")).rejects.toMatchObject({ code: "DOWNLOAD_FAILED", status: 502 });
  });
});
