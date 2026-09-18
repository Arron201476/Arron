// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../../api";
import { MemoryPreferences } from "./MemoryPreferences";
const initial = { project_id: "p", user_id: "u", revision: 0, archive_enabled: false, generate_enabled: false };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it("requires archive consent before automatic generation and verifies the save", async () => {
  vi.spyOn(api, "getMemoryPreferences").mockResolvedValue(initial);
  const save = vi.spyOn(api, "updateMemoryPreferences").mockImplementation(async (_project, command) => ({ ...initial, ...command, revision: 1 }));
  render(<MemoryPreferences projectID="p" userID="u" editable />);
  await waitFor(() => expect(screen.getByLabelText("允许将本作品对话归档为本人记忆来源")).toHaveProperty("disabled", false));
  expect(screen.getByLabelText("允许从归档自动生成私有记忆")).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByLabelText("允许将本作品对话归档为本人记忆来源"));
  fireEvent.click(screen.getByLabelText("允许从归档自动生成私有记忆"));
  fireEvent.click(screen.getByRole("button", { name: "保存授权" }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
  expect(save.mock.calls[0][1]).toMatchObject({ archive_enabled: true, generate_enabled: true, expected_revision: 0 });
  await waitFor(() => expect(screen.getByRole("button", { name: "保存授权" })).toHaveProperty("disabled", true));
});
it("retries unchanged consent after a lost response", async () => {
  vi.spyOn(api, "getMemoryPreferences").mockResolvedValue(initial);
  const save = vi.spyOn(api, "updateMemoryPreferences").mockRejectedValueOnce(new Error("lost"))
    .mockImplementation(async (_project, command) => ({ ...initial, ...command, revision: 1 }));
  render(<MemoryPreferences projectID="p" userID="u" editable />);
  await waitFor(() => expect(screen.getByLabelText("允许将本作品对话归档为本人记忆来源")).toHaveProperty("disabled", false));
  fireEvent.click(screen.getByLabelText("允许将本作品对话归档为本人记忆来源"));
  fireEvent.click(screen.getByRole("button", { name: "保存授权" }));
  await screen.findByText("lost");
  expect(screen.getByLabelText("允许将本作品对话归档为本人记忆来源")).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "重试原授权请求" }));
  await waitFor(() => expect(save).toHaveBeenCalledTimes(2));
  expect(save.mock.calls[1]).toEqual(save.mock.calls[0]);
});
it("does not treat another owner's settings as consent", async () => {
  vi.spyOn(api, "getMemoryPreferences").mockResolvedValue({ ...initial, user_id: "other", archive_enabled: true });
  render(<MemoryPreferences projectID="p" userID="u" editable />);
  await screen.findByRole("alert");
  expect(screen.getByRole("button", { name: "保存授权" })).toHaveProperty("disabled", true);
});
