import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Project } from "../../api/types";
import { ProjectPicker } from "./ProjectPicker";

const project: Project = { project_id: "project_1", title: "项目1", source_mode: "novel", status: "completed" };

describe("ProjectPicker", () => {
  it("requires confirmation before deleting a completed project", async () => {
    const onDelete = vi.fn().mockResolvedValue(undefined);
    render(<ProjectPicker projects={[project]} onCreate={vi.fn()} onDelete={onDelete} onOpen={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "删除作品：项目1" }));
    expect(screen.getByRole("alertdialog")).toHaveTextContent("无法撤销");
    fireEvent.click(screen.getByRole("button", { name: "确认删除" }));
    expect(onDelete).toHaveBeenCalledWith(project);
  });

  it("prevents deletion while a project is running", () => {
    render(<ProjectPicker projects={[{ ...project, status: "running" }]} onCreate={vi.fn()} onDelete={vi.fn()} onOpen={vi.fn()} />);
    expect(screen.getByRole("button", { name: "删除作品：项目1" })).toBeDisabled();
  });
});
