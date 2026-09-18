// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { ArtifactDocument } from "./ArtifactDocument";

afterEach(cleanup);

describe("ArtifactDocument episode plan directory", () => {
  it("paginates the directory and shows only the selected episode detail", () => {
    render(<ArtifactDocument artifactType="episode_cards" presentation={{ artifact_type: "episode_cards", label: "分集规划", description: "逐集规划", renderer: "episode_plan", editable: true, preferred_fields: ["episodes", "continuity_delta"], navigation: { order: 1, group_mode: "single", visibility: "user" }, available_actions: [] }} payload={{
      episodes: Array.from({ length: 14 }, (_, index) => ({
        episode_no: index + 1,
        core_event: `第 ${index + 1} 集事件`,
        episode_goal: `第 ${index + 1} 集细节`,
      })),
      continuity_delta: "全局衔接",
    }} />);

    expect(screen.getByRole("navigation", { name: "分集规划目录" })).toBeTruthy();
    expect(screen.getByText("第 1 集细节")).toBeTruthy();
    expect(screen.queryByText("第 13 集细节")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    fireEvent.click(screen.getByRole("button", { name: /13.*第 13 集事件/ }));

    expect(screen.getByText("第 13 集细节")).toBeTruthy();
    expect(screen.queryByText("第 1 集细节")).toBeNull();
    expect(screen.getByText("2 / 2")).toBeTruthy();
  });
});
