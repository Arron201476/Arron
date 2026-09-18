import { describe, expect, it } from "vitest";
import config from "../vite.config";

describe("development API proxy", () => {
  it("preserves the browser Host for backend same-origin protection", () => {
    expect(config.server?.proxy?.["/api"]).toMatchObject({
      target: expect.any(String),
      changeOrigin: false,
    });
  });
});
