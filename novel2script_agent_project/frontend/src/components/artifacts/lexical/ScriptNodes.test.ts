import { $createTextNode, $getRoot, $getSelection, $isRangeSelection, createEditor } from "lexical";
import { describe, expect, it } from "vitest";
import { $createScriptLineNode, $isScriptLineNode, ScriptLineNode } from "./ScriptNodes";

describe("ScriptLineNode", () => {
  it("creates a structured sibling line when a paragraph is inserted", async () => {
    const editor = createEditor({
      namespace: "script-line-enter-test",
      nodes: [ScriptLineNode],
      onError(error) { throw error; },
    });

    await new Promise<void>((resolve) => editor.update(() => {
      const line = $createScriptLineNode("1", "scene_1", "line_1", "dialogue", "主角");
      line.append($createTextNode("原台词"));
      $getRoot().append(line);
      line.selectEnd();
    }, { onUpdate: resolve }));

    await new Promise<void>((resolve) => editor.update(() => {
      const selection = $getSelection();
      if (!$isRangeSelection(selection)) throw new Error("range selection not found");
      selection.insertParagraph();
    }, { onUpdate: resolve }));

    editor.getEditorState().read(() => {
      const lines = $getRoot().getChildren().filter($isScriptLineNode);
      expect(lines).toHaveLength(2);
      expect(lines[1].getLineID()).toContain("scene_1-manual-");
      expect(lines[1].getBlockType()).toBe("dialogue");
      expect(lines[1].getSpeaker()).toBeUndefined();
    });
  });
});
