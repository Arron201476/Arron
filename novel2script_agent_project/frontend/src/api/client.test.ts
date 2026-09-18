import { afterEach, describe, expect, it, vi } from "vitest";
import { listProjects } from "./client";

describe("API client retry policy", () => {
  afterEach(() => vi.restoreAllMocks());

  it("does not retry a non-retryable 404 response", async () => {
    const fetchMock = vi.spyOn(window, "fetch").mockResolvedValue(new Response(JSON.stringify({
      error: { code: "NOT_FOUND", message: "not found", recoverable: true, retryable: false },
    }), { status: 404, headers: { "Content-Type": "application/json" } }));

    await expect(listProjects()).rejects.toMatchObject({ status: 404 });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
