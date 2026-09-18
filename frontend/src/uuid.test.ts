import { afterEach, describe, expect, it, vi } from "vitest";

import { createUUID } from "./uuid";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("createUUID", () => {
  it("uses the platform UUID API when available", () => {
    vi.stubGlobal("crypto", { randomUUID: () => "00000000-0000-4000-8000-000000000001" });

    expect(createUUID()).toBe("00000000-0000-4000-8000-000000000001");
  });

  it("creates an RFC 4122 version 4 UUID without randomUUID", () => {
    vi.stubGlobal("crypto", { getRandomValues: (bytes: Uint8Array) => bytes.fill(0) });

    expect(createUUID()).toBe("00000000-0000-4000-8000-000000000000");
  });
});
