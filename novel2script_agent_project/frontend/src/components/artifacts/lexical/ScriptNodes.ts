import {
  $applyNodeReplacement,
  ElementNode,
  TextNode,
  type EditorConfig,
  type LexicalNode,
  type NodeKey,
  type RangeSelection,
  type SerializedElementNode,
  type SerializedTextNode,
  type Spread,
} from "lexical";
import type { ScriptBlockType } from "../../../api/types";

type SerializedEpisodeHeadingNode = Spread<{ episodeID: string; episodeNo: number }, SerializedElementNode>;
type SerializedSceneHeadingNode = Spread<{ episodeID: string; sceneID: string; sceneNo: number }, SerializedElementNode>;
type SerializedScriptLineNode = Spread<{
  episodeID: string;
  sceneID: string;
  lineID: string;
  blockType: ScriptBlockType;
  speaker?: string;
}, SerializedElementNode>;
type SerializedCommentNode = Spread<{ commentID: string; comment: string }, SerializedTextNode>;
type SerializedSuggestionNode = Spread<{ suggestionID: string; originalText: string }, SerializedTextNode>;

export class CommentNode extends TextNode {
  __commentID: string;
  __comment: string;
  static getType() { return "script-comment"; }
  static clone(node: CommentNode) { return new CommentNode(node.__text, node.__commentID, node.__comment, node.__key); }
  constructor(text: string, commentID: string, comment: string, key?: NodeKey) { super(text, key); this.__commentID = commentID; this.__comment = comment; }
  createDOM(config: EditorConfig) { const dom = super.createDOM(config); dom.classList.add("script-comment"); dom.dataset.commentId = this.__commentID; dom.title = this.__comment; return dom; }
  updateDOM(previous: CommentNode, dom: HTMLElement, config: EditorConfig) { const changed = super.updateDOM(previous as this, dom, config); dom.classList.add("script-comment"); dom.dataset.commentId = this.__commentID; dom.title = this.__comment; return changed; }
  exportJSON(): SerializedCommentNode { return { ...super.exportJSON(), type: "script-comment", version: 1, commentID: this.__commentID, comment: this.__comment }; }
  static importJSON(value: SerializedCommentNode) { return $createCommentNode(value.text, value.commentID, value.comment); }
}

export class SuggestionNode extends TextNode {
  __suggestionID: string;
  __originalText: string;
  static getType() { return "script-suggestion"; }
  static clone(node: SuggestionNode) { return new SuggestionNode(node.__text, node.__originalText, node.__suggestionID, node.__key); }
  constructor(text: string, originalText: string, suggestionID: string, key?: NodeKey) { super(text, key); this.__originalText = originalText; this.__suggestionID = suggestionID; }
  createDOM(config: EditorConfig) { const dom = super.createDOM(config); dom.classList.add("script-suggestion"); dom.dataset.suggestionId = this.__suggestionID; dom.title = `原文：${this.__originalText}`; return dom; }
  updateDOM(previous: SuggestionNode, dom: HTMLElement, config: EditorConfig) { const changed = super.updateDOM(previous as this, dom, config); dom.classList.add("script-suggestion"); dom.dataset.suggestionId = this.__suggestionID; dom.title = `原文：${this.__originalText}`; return changed; }
  exportJSON(): SerializedSuggestionNode { return { ...super.exportJSON(), type: "script-suggestion", version: 1, suggestionID: this.__suggestionID, originalText: this.__originalText }; }
  static importJSON(value: SerializedSuggestionNode) { return $createSuggestionNode(value.originalText, value.text, value.suggestionID); }
  getOriginalText() { return this.__originalText; }
}

export class EpisodeHeadingNode extends ElementNode {
  __episodeID: string;
  __episodeNo: number;

  static getType() { return "episode-heading"; }
  static clone(node: EpisodeHeadingNode) { return new EpisodeHeadingNode(node.__episodeID, node.__episodeNo, node.__key); }
  constructor(episodeID: string, episodeNo: number, key?: NodeKey) {
    super(key);
    this.__episodeID = episodeID;
    this.__episodeNo = episodeNo;
  }
  createDOM(_config: EditorConfig) {
    const dom = document.createElement("h2");
    dom.dataset.episodeId = this.__episodeID;
    return dom;
  }
  updateDOM() { return false; }
  exportJSON(): SerializedEpisodeHeadingNode {
    return { ...super.exportJSON(), type: "episode-heading", version: 1, episodeID: this.__episodeID, episodeNo: this.__episodeNo };
  }
  static importJSON(value: SerializedEpisodeHeadingNode) { return $createEpisodeHeadingNode(value.episodeID, value.episodeNo); }
  getEpisodeID() { return this.__episodeID; }
}

export class SceneHeadingNode extends ElementNode {
  __episodeID: string;
  __sceneID: string;
  __sceneNo: number;

  static getType() { return "scene-heading"; }
  static clone(node: SceneHeadingNode) { return new SceneHeadingNode(node.__episodeID, node.__sceneID, node.__sceneNo, node.__key); }
  constructor(episodeID: string, sceneID: string, sceneNo: number, key?: NodeKey) {
    super(key);
    this.__episodeID = episodeID;
    this.__sceneID = sceneID;
    this.__sceneNo = sceneNo;
  }
  createDOM(_config: EditorConfig) {
    const dom = document.createElement("h3");
    dom.dataset.episodeId = this.__episodeID;
    dom.dataset.sceneId = this.__sceneID;
    return dom;
  }
  updateDOM() { return false; }
  exportJSON(): SerializedSceneHeadingNode {
    return { ...super.exportJSON(), type: "scene-heading", version: 1, episodeID: this.__episodeID, sceneID: this.__sceneID, sceneNo: this.__sceneNo };
  }
  static importJSON(value: SerializedSceneHeadingNode) { return $createSceneHeadingNode(value.episodeID, value.sceneID, value.sceneNo); }
  getSceneID() { return this.__sceneID; }
  getSceneNo() { return this.__sceneNo; }
}

export class ScriptLineNode extends ElementNode {
  __episodeID: string;
  __sceneID: string;
  __lineID: string;
  __blockType: ScriptBlockType;
  __speaker?: string;

  static getType() { return "script-line"; }
  static clone(node: ScriptLineNode) {
    return new ScriptLineNode(node.__episodeID, node.__sceneID, node.__lineID, node.__blockType, node.__speaker, node.__key);
  }
  constructor(episodeID: string, sceneID: string, lineID: string, blockType: ScriptBlockType, speaker?: string, key?: NodeKey) {
    super(key);
    this.__episodeID = episodeID;
    this.__sceneID = sceneID;
    this.__lineID = lineID;
    this.__blockType = blockType;
    this.__speaker = speaker;
  }
  createDOM(_config: EditorConfig) {
    const dom = document.createElement("p");
    applyLineDOM(this, dom);
    return dom;
  }
  updateDOM(_previous: ScriptLineNode, dom: HTMLElement) {
    applyLineDOM(this, dom);
    return false;
  }
  exportJSON(): SerializedScriptLineNode {
    return {
      ...super.exportJSON(), type: "script-line", version: 1,
      episodeID: this.__episodeID, sceneID: this.__sceneID, lineID: this.__lineID,
      blockType: this.__blockType, speaker: this.__speaker,
    };
  }
  static importJSON(value: SerializedScriptLineNode) {
    return $createScriptLineNode(value.episodeID, value.sceneID, value.lineID, value.blockType, value.speaker);
  }
  getEpisodeID() { return this.__episodeID; }
  getSceneID() { return this.__sceneID; }
  getLineID() { return this.__lineID; }
  getBlockType() { return this.__blockType; }
  getSpeaker() { return this.__speaker; }
  setBlockType(value: ScriptBlockType) { this.getWritable().__blockType = value; }
  insertNewAfter(_selection: RangeSelection, restoreSelection = true) {
    const nextLine = $createScriptLineNode(
      this.__episodeID,
      this.__sceneID,
      nextManualLineID(this.__sceneID),
      this.__blockType,
    );
    this.insertAfter(nextLine, restoreSelection);
    return nextLine;
  }
}

let manualLineCounter = 0;

function nextManualLineID(sceneID: string) {
  manualLineCounter += 1;
  return `${sceneID || "scene"}-manual-${Date.now().toString(36)}-${manualLineCounter.toString(36)}`;
}

function applyLineDOM(node: ScriptLineNode, dom: HTMLElement) {
  dom.className = `script-line ${node.__blockType}`;
  dom.dataset.episodeId = node.__episodeID;
  dom.dataset.sceneId = node.__sceneID;
  dom.dataset.lineId = node.__lineID;
  dom.dataset.blockType = node.__blockType;
  if (node.__speaker) dom.dataset.speaker = node.__speaker;
  else delete dom.dataset.speaker;
}

export function $createEpisodeHeadingNode(episodeID: string, episodeNo: number) {
  return $applyNodeReplacement(new EpisodeHeadingNode(episodeID, episodeNo));
}
export function $createSceneHeadingNode(episodeID: string, sceneID: string, sceneNo: number) {
  return $applyNodeReplacement(new SceneHeadingNode(episodeID, sceneID, sceneNo));
}
export function $createScriptLineNode(episodeID: string, sceneID: string, lineID: string, blockType: ScriptBlockType, speaker?: string) {
  return $applyNodeReplacement(new ScriptLineNode(episodeID, sceneID, lineID, blockType, speaker));
}
export function $createCommentNode(text: string, commentID: string, comment: string) { return $applyNodeReplacement(new CommentNode(text, commentID, comment)); }
export function $createSuggestionNode(originalText: string, suggestedText: string, suggestionID: string) { return $applyNodeReplacement(new SuggestionNode(suggestedText, originalText, suggestionID)); }
export function $isEpisodeHeadingNode(node: LexicalNode | null | undefined): node is EpisodeHeadingNode { return node instanceof EpisodeHeadingNode; }
export function $isSceneHeadingNode(node: LexicalNode | null | undefined): node is SceneHeadingNode { return node instanceof SceneHeadingNode; }
export function $isScriptLineNode(node: LexicalNode | null | undefined): node is ScriptLineNode { return node instanceof ScriptLineNode; }
export function $isSuggestionNode(node: LexicalNode | null | undefined): node is SuggestionNode { return node instanceof SuggestionNode; }
