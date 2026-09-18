// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ScriptArtifactEditor, scenesFromScript } from "./ScriptArtifactEditor";

afterEach(cleanup);

describe("ScriptArtifactEditor", () => {
  it("updates script text and keeps structured scenes in the artifact payload", () => {
    const onChange = vi.fn();
    render(<ScriptArtifactEditor payload={{ episode_no: 1, script_text: "第1集\n场1-1 旧宅 夜 内\n△门被推开。" }} episodeNo={1} dirty saving={false} failure="" onChange={onChange} onSave={vi.fn()} onCancel={vi.fn()} />);

    const editor = screen.getByRole("textbox", { name: "剧本正文编辑器" });
    editor.textContent = "第1集\n场1-1 旧宅 夜 内\n林澈：谁在那里？";
    fireEvent.input(editor);

    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({
      episode_no: 1,
      script_text: "第1集\n场1-1 旧宅 夜 内\n林澈：谁在那里？",
      scenes: [expect.objectContaining({ heading: "场1-1 旧宅 夜 内", blocks: [expect.objectContaining({ block_type: "dialogue", speaker: "林澈", text: "谁在那里？" })] })],
    }));
  });

  it("keeps save and cancel as editor-level commands", () => {
    const onSave = vi.fn();
    const onCancel = vi.fn();
    render(<ScriptArtifactEditor payload={{ script_text: "第1集" }} episodeNo={1} dirty saving={false} failure="" onChange={vi.fn()} onSave={onSave} onCancel={onCancel} />);

    fireEvent.click(screen.getByRole("button", { name: "保存新版本" }));
    fireEvent.click(screen.getByRole("button", { name: "撤销修改" }));
    expect(onSave).toHaveBeenCalledOnce();
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it("offers the expected script formatting controls", () => {
    Object.defineProperty(document, "execCommand", { configurable: true, value: vi.fn() });
    render(<ScriptArtifactEditor payload={{ script_text: "正文" }} episodeNo={1} dirty saving={false} failure="" onChange={vi.fn()} onSave={vi.fn()} onCancel={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", { name: "加粗" }));
    expect(document.execCommand).toHaveBeenCalledWith("bold", false);
    expect(screen.getByRole("button", { name: "斜体" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "下划线" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "删除线" })).toBeTruthy();
  });
  it("hydrates the current episode when its payload arrives after the editor mounts", () => {
    const view = render(<ScriptArtifactEditor payload={{}} episodeNo={1} dirty={false} saving={false} failure="" onChange={vi.fn()} onSave={vi.fn()} onCancel={vi.fn()} />);
    const editor = screen.getByRole("textbox");
    expect(editor.textContent).toBe("");

    view.rerender(<ScriptArtifactEditor payload={{ script_text: "episode 1\nscene 1" }} episodeNo={1} dirty={false} saving={false} failure="" onChange={vi.fn()} onSave={vi.fn()} onCancel={vi.fn()} />);
    expect(editor.textContent).toContain("scene 1");
  });
});

describe("scenesFromScript", () => {
  it("splits scene headings without treating episode headings as scenes", () => {
    expect(scenesFromScript("第1集\n场1-1 街口 日 外\n△人群散开。\n甲：快走。", 1)).toEqual([
      { scene_id: "scene-1-1", heading: "场1-1 街口 日 外", blocks: [
        { line_id: "scene-1-1-line-1", block_type: "action", text: "△人群散开。" },
        { line_id: "scene-1-1-line-2", block_type: "dialogue", speaker: "甲", text: "快走。" },
      ] },
    ]);
  });
});
