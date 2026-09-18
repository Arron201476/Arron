import { LexicalComposer } from "@lexical/react/LexicalComposer";
import { useLexicalComposerContext } from "@lexical/react/LexicalComposerContext";
import { ContentEditable } from "@lexical/react/LexicalContentEditable";
import { HistoryPlugin } from "@lexical/react/LexicalHistoryPlugin";
import { RichTextPlugin } from "@lexical/react/LexicalRichTextPlugin";
import {
  $createParagraphNode, $createTextNode, $getRoot, $getSelection, $isRangeSelection,
  COMMAND_PRIORITY_LOW, FORMAT_TEXT_COMMAND, REDO_COMMAND, SELECTION_CHANGE_COMMAND, UNDO_COMMAND,
  type EditorState, type LexicalNode,
} from "lexical";
import { Redo2, Save, Undo2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { Artifact, ScriptBlockType, ScriptTextMark, SelectionContext } from "../../../api/types";
import {
  $createEpisodeHeadingNode, $createSceneHeadingNode, $createScriptLineNode,
  CommentNode, SuggestionNode,
  $isEpisodeHeadingNode, $isSceneHeadingNode, $isScriptLineNode,
  EpisodeHeadingNode, SceneHeadingNode, ScriptLineNode,
} from "./ScriptNodes";
import { buildScriptSelectionContext } from "./scriptSelection";

export interface LexicalSceneInput {
  sceneId: string;
  heading: string;
  lines: Array<{ lineId: string; blockType: ScriptBlockType; text: string; speaker?: string }>;
}

export interface LexicalScriptDraft {
  editorState: Record<string, unknown>;
  title: string;
  scenes: Array<Record<string, unknown>>;
  scriptText: string;
}

interface Props {
  artifact: Artifact;
  episodeId: string;
  title: string;
  scenes: LexicalSceneInput[];
  initialEditorState?: Record<string, unknown>;
  readOnly?: boolean;
  dirty: boolean;
  isSaving: boolean;
  saveStatus: string;
  onDirtyChange: (dirty: boolean) => void;
  onSave: (draft: LexicalScriptDraft) => void;
  onSelectionChange?: (selection: SelectionContext | null) => void;
  toolbarEnd?: ReactNode;
}

const scriptEditorTheme = {
  text: {
    bold: "script-text-bold",
    italic: "script-text-italic",
    underline: "script-text-underline",
    strikethrough: "script-text-strikethrough",
    underlineStrikethrough: "script-text-underline-strikethrough",
  },
};

export function ScriptLexicalEditor(props: Props) {
  const initialState = useMemo(() => props.initialEditorState
    ? JSON.stringify(props.initialEditorState)
    : () => loadEpisode(props.episodeId, props.title, props.scenes),
  [props.episodeId, props.initialEditorState, props.scenes, props.title]);
  return (
    <LexicalComposer initialConfig={{
      namespace: `Novel2Script-${props.artifact.artifact_id}-${props.artifact.version}`,
      nodes: [EpisodeHeadingNode, SceneHeadingNode, ScriptLineNode, CommentNode, SuggestionNode],
      theme: scriptEditorTheme,
      editable: !props.readOnly,
      editorState: initialState,
      onError(error) { throw error; },
    }}>
      <EditorToolbar {...props} />
      <RichTextPlugin
        contentEditable={<ContentEditable aria-label="剧本正文编辑器" className="script-page script-page-editable lexical-production-editor" />}
        placeholder={<div className="lexical-placeholder">输入剧本正文</div>}
        ErrorBoundary={LexicalErrorBoundary}
      />
      <HistoryPlugin />
      <EditorStateBridge {...props} />
    </LexicalComposer>
  );
}

function EditorToolbar(props: Props) {
  const [editor] = useLexicalComposerContext();
  const [activeFormats, setActiveFormats] = useState<Record<string, boolean>>({});
  const requestSave = () => editor.getEditorState().read(() => props.onSave(readDraft(editor.getEditorState())));

  useEffect(() => {
    const syncFormats = () => {
      editor.getEditorState().read(() => {
        const selection = $getSelection();
        setActiveFormats($isRangeSelection(selection) ? {
          bold: selection.hasFormat("bold"),
          italic: selection.hasFormat("italic"),
          underline: selection.hasFormat("underline"),
          strikethrough: selection.hasFormat("strikethrough"),
        } : {});
      });
    };
    const unregisterSelection = editor.registerCommand(SELECTION_CHANGE_COMMAND, () => {
      syncFormats();
      return false;
    }, COMMAND_PRIORITY_LOW);
    const unregisterUpdate = editor.registerUpdateListener(syncFormats);
    return () => {
      unregisterSelection();
      unregisterUpdate();
    };
  }, [editor]);

  return (
    <div className="script-toolbar" aria-label="正文编辑工具栏">
      <button aria-label="撤销" disabled={props.readOnly} onClick={() => editor.dispatchCommand(UNDO_COMMAND, undefined)} type="button"><Undo2 size={15} /></button>
      <button aria-label="重做" disabled={props.readOnly} onClick={() => editor.dispatchCommand(REDO_COMMAND, undefined)} type="button"><Redo2 size={15} /></button>
      {(["bold", "italic", "underline", "strikethrough"] as const).map((format) => (
        <button aria-label={formatLabel(format)} aria-pressed={Boolean(activeFormats[format])} className={`format-button ${format} ${activeFormats[format] ? "active" : ""}`} disabled={props.readOnly} key={format} onClick={() => editor.dispatchCommand(FORMAT_TEXT_COMMAND, format)} title={formatLabel(format)} type="button">{format === "strikethrough" ? "S" : format[0].toUpperCase()}</button>
      ))}
      <button aria-label={props.readOnly ? "只读聚合视图" : props.isSaving ? "保存中" : "保存"} disabled={props.readOnly || props.isSaving || !props.dirty} onClick={requestSave} type="button"><Save size={15} /></button>
      <span className="script-save-status">{props.saveStatus || (props.readOnly ? "只读" : "已保存")}</span>
      {props.toolbarEnd ? <><span className="script-toolbar-spacer" />{props.toolbarEnd}</> : null}
    </div>
  );
}

function EditorStateBridge(props: Props) {
  const [editor] = useLexicalComposerContext();
  const initialFingerprint = useRef("");
  const selectionCallback = useRef(props.onSelectionChange);
  const dirtyCallback = useRef(props.onDirtyChange);
  const artifactRef = useRef(props.artifact);
  selectionCallback.current = props.onSelectionChange;
  dirtyCallback.current = props.onDirtyChange;
  artifactRef.current = props.artifact;

  useEffect(() => {
    initialFingerprint.current = JSON.stringify(editor.getEditorState().toJSON());
    dirtyCallback.current(false);
    return editor.registerUpdateListener(({ editorState }) => {
      const dirty = JSON.stringify(editorState.toJSON()) !== initialFingerprint.current;
      dirtyCallback.current(dirty);
      editorState.read(() => selectionCallback.current?.(selectionContext(artifactRef.current)));
    });
  }, [editor]);

  return null;
}

function loadEpisode(episodeId: string, title: string, scenes: LexicalSceneInput[]) {
  const root = $getRoot();
  root.clear();
  const episodeHeading = $createEpisodeHeadingNode(episodeId, Number(episodeId) || 1);
  episodeHeading.append($createTextNode(title));
  root.append(episodeHeading);
  for (const [sceneIndex, scene] of scenes.entries()) {
    const sceneHeading = $createSceneHeadingNode(episodeId, scene.sceneId, sceneIndex + 1);
    sceneHeading.append($createTextNode(scene.heading));
    root.append(sceneHeading);
    for (const line of scene.lines) {
      const lineNode = $createScriptLineNode(episodeId, scene.sceneId, line.lineId, line.blockType, line.speaker);
      lineNode.append($createTextNode(line.text));
      root.append(lineNode);
    }
  }
  if (scenes.length === 0) root.append($createParagraphNode().append($createTextNode("")));
}

function readDraft(editorState: EditorState): LexicalScriptDraft {
  const scenes: Array<Record<string, unknown>> = [];
  let title = "";
  let currentScene: Record<string, unknown> | null = null;
  for (const node of $getRoot().getChildren()) {
    if ($isEpisodeHeadingNode(node)) {
      title = node.getTextContent();
    } else if ($isSceneHeadingNode(node)) {
      currentScene = { scene_id: node.getSceneID(), scene_no: node.getSceneNo(), heading: node.getTextContent(), blocks: [] as unknown[] };
      scenes.push(currentScene);
    } else if ($isScriptLineNode(node)) {
      if (!currentScene) {
        currentScene = { scene_id: node.getSceneID(), heading: "场景", blocks: [] as unknown[] };
        scenes.push(currentScene);
      }
      const blocks = currentScene.blocks as unknown[];
      blocks.push({ line_id: node.getLineID(), block_type: node.getBlockType(), speaker: node.getSpeaker(), text: node.getTextContent(), marks: marksFromLine(node) });
    }
  }
  const scriptLines = [title];
  for (const scene of scenes) {
    scriptLines.push(String(scene.heading || ""));
    for (const value of scene.blocks as Array<Record<string, unknown>>) {
      const speaker = value.speaker ? `${value.speaker}: ` : "";
      scriptLines.push(speaker + String(value.text || ""));
    }
  }
  return { editorState: editorState.toJSON() as unknown as Record<string, unknown>, title, scenes, scriptText: scriptLines.filter(Boolean).join("\n") };
}

function marksFromLine(line: ScriptLineNode): ScriptTextMark[] {
  const marks: ScriptTextMark[] = [];
  let offset = 0;
  for (const node of line.getAllTextNodes()) {
    const length = node.getTextContentSize();
    for (const [format, type] of [["bold", "bold"], ["italic", "italic"], ["underline", "underline"], ["strikethrough", "strike"]] as const) {
      if (node.hasFormat(format)) marks.push({ type, offset_range: [offset, offset + length] });
    }
    offset += length;
  }
  return marks;
}

function selectionContext(artifact: Artifact): SelectionContext | null {
  const selection = $getSelection();
  if (!$isRangeSelection(selection) || selection.isCollapsed()) return null;
  const anchorLine = findParentLine(selection.anchor.getNode());
  const focusLine = findParentLine(selection.focus.getNode());
  if (!anchorLine || !focusLine) return null;
  const lines = $getRoot().getChildren().filter($isScriptLineNode).map((line) => ({
    episodeId: line.getEpisodeID(), sceneId: line.getSceneID(), lineId: line.getLineID(), blockType: line.getBlockType(), text: line.getTextContent(),
  }));
  return buildScriptSelectionContext(
    artifact,
    lines,
    { lineId: anchorLine.getLineID(), offset: absolutePointOffset(anchorLine, selection.anchor.getNode(), selection.anchor.offset) },
    { lineId: focusLine.getLineID(), offset: absolutePointOffset(focusLine, selection.focus.getNode(), selection.focus.offset) },
  );
}

function absolutePointOffset(line: ScriptLineNode, pointNode: LexicalNode, pointOffset: number) {
  let offset = 0;
  for (const textNode of line.getAllTextNodes()) {
    if (textNode.getKey() === pointNode.getKey()) return offset + pointOffset;
    offset += textNode.getTextContentSize();
  }
  return offset;
}

function findParentLine(node: LexicalNode | null): ScriptLineNode | null {
  let current = node;
  while (current) {
    if ($isScriptLineNode(current)) return current;
    current = current.getParent();
  }
  return null;
}

function formatLabel(value: string) { return ({ bold: "加粗", italic: "斜体", underline: "下划线", strikethrough: "删除线" } as Record<string, string>)[value] || value; }
function LexicalErrorBoundary({ children }: { children: ReactNode }) { return children; }
