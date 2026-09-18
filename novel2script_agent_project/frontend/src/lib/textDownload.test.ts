import { describe, expect, it } from "vitest";
import { sanitizeFileName } from "./textDownload";

describe("sanitizeFileName", () => {
  it("keeps Chinese product names and replaces Windows reserved characters", () => {
    expect(sanitizeFileName(" 项目1_故事圣经 ")).toBe("项目1_故事圣经");
    expect(sanitizeFileName("项目:1/剧本?. ")).toBe("项目_1_剧本_");
  });
});
