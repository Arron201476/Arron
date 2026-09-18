import { describe, expect, it } from "vitest";
import { skillArchiveHash } from "./skillPackage";

describe("Skill archive identity", () => {
  it("hashes exact file bytes independently of filename and MIME", async () => {
    const expected = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad";
    expect(await skillArchiveHash(new File([new Uint8Array([97, 98, 99])], "中文.zip"))).toBe(expected);
    expect(await skillArchiveHash(new File(["abc"], "renamed.zip", { type: "application/octet-stream" }))).toBe(expected);
    expect(await skillArchiveHash(new File([new Uint8Array([97, 98, 99, 0])], "中文.zip"))).not.toBe(expected);
  });
  it("rejects oversized archives before reading bytes", async () => {
    const file = new File([], "large.zip");
    Object.defineProperty(file, "size", { value: 20 * 1024 * 1024 + 1 });
    await expect(skillArchiveHash(file)).rejects.toMatchObject({ code: "SKILL_ARCHIVE_TOO_LARGE" });
  });
});
