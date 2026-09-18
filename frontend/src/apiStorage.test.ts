import { afterEach, describe, expect, it, vi } from "vitest";

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.resetModules(); });

describe("API initialization without session storage", () => {
  it.each(["getItem", "setItem"])("keeps read-only API access available when %s is denied", async (method) => {
    vi.resetModules();
    vi.stubGlobal("sessionStorage", { getItem: () => null, setItem: () => {}, [method]: () => { throw new DOMException("disabled", "SecurityError"); } });
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ data: { items: [] } }), { status: 200 }));
    const { api } = await import("./api");
    expect(await api.listSkills()).toEqual([]);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/skills?include_uninstalled=false");
  });

  it("retains an existing client ID even when writing it back is denied", async () => {
    vi.resetModules();
    vi.stubGlobal("sessionStorage", { getItem: () => "original-client", setItem: () => { throw new DOMException("disabled", "SecurityError"); } });
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ data: { diagnostics: [] } }), { status: 200 }));
    const { api } = await import("./api");
    await api.refreshSkills();
    expect(new Headers(fetchMock.mock.calls[0][1]?.headers).get("X-Client-Instance-ID")).toBe("original-client");
  });
});
