import { Bold, Italic, Redo2, Save, Strikethrough, Underline, Undo2, X } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";

type FormatCommand = "bold" | "italic" | "underline" | "strikeThrough";

type Props = {
  editorID: string;
  editorLabel: string;
  toolbarLabel: string;
  text: string;
  dirty: boolean;
  saving: boolean;
  locked?: boolean;
  saveStatus?: string;
  failure: string;
  pageClassName?: string;
  bodyClassName?: string;
  header?: ReactNode;
  renderHTML?: (text: string) => string;
  onCommit: (text: string, html: string) => void;
  onSelection: (editor: HTMLDivElement) => void;
  onSave: () => void;
  onCancel: () => void;
};

export function TextArtifactEditor({ editorID, editorLabel, toolbarLabel, text, dirty, saving, locked = false, saveStatus, failure, pageClassName = "", bodyClassName = "", header, renderHTML, onCommit, onSelection, onSave, onCancel }: Props) {
  const editorRef = useRef<HTMLDivElement>(null);
  const initialized = useRef(false);
  const [formats, setFormats] = useState<Record<FormatCommand, boolean>>({ bold: false, italic: false, underline: false, strikeThrough: false });

  useEffect(() => {
    const editor = editorRef.current;
    if (!editor || dirty && initialized.current) return;
    initialized.current = true;
    const currentText = editorText(editor);
    if (currentText === text.trimEnd()) return;
    if (renderHTML) editor.innerHTML = renderHTML(text);
    else editor.textContent = text;
  }, [dirty, renderHTML, text]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (!(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== "s") return;
      event.preventDefault();
      if (dirty && !saving) onSave();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [dirty, onSave, saving]);

  useEffect(() => {
    const update = () => {
      if (!editorRef.current?.contains(document.activeElement)) return;
      setFormats({
        bold: queryCommand("bold"),
        italic: queryCommand("italic"),
        underline: queryCommand("underline"),
        strikeThrough: queryCommand("strikeThrough"),
      });
    };
    document.addEventListener("selectionchange", update);
    return () => document.removeEventListener("selectionchange", update);
  }, []);

  const commit = () => {
    const editor = editorRef.current;
    if (!editor || saving || locked) return;
    onCommit(editorText(editor), editor.innerHTML);
  };
  const execute = (command: FormatCommand | "undo" | "redo") => {
    if (saving || locked) return;
    editorRef.current?.focus();
    if (typeof document.execCommand === "function") document.execCommand(command, false);
    commit();
    setFormats((current) => command in current ? { ...current, [command]: queryCommand(command as FormatCommand) } : current);
  };
  const formatButtons: Array<{ command: FormatCommand; label: string; icon: typeof Bold }> = [
    { command: "bold", label: "加粗", icon: Bold },
    { command: "italic", label: "斜体", icon: Italic },
    { command: "underline", label: "下划线", icon: Underline },
    { command: "strikeThrough", label: "删除线", icon: Strikethrough },
  ];

  return (
    <div className="script-editor-shell text-artifact-editor-shell">
      <div className="script-editor-toolbar" aria-label={toolbarLabel}>
        <div className="script-editor-tools">
          <div className="script-tool-group" aria-label="历史操作">
            <button type="button" className="icon-button" aria-label="撤销" title="撤销" disabled={saving || locked} onClick={() => execute("undo")}><Undo2 size={16} /></button>
            <button type="button" className="icon-button" aria-label="重做" title="重做" disabled={saving || locked} onClick={() => execute("redo")}><Redo2 size={16} /></button>
          </div>
          <span className="script-tool-divider" />
          <div className="script-tool-group" aria-label="文字格式">
            {formatButtons.map(({ command, label, icon: Icon }) => <button type="button" key={command} className={`icon-button ${formats[command] ? "active" : ""}`} aria-label={label} title={label} aria-pressed={formats[command]} disabled={saving || locked} onMouseDown={(event) => event.preventDefault()} onClick={() => execute(command)}><Icon size={16} /></button>)}
          </div>
          <span className={`script-save-state ${dirty ? "dirty" : ""}`}>{saving ? "正在保存" : saveStatus ?? (dirty ? "有未保存修改" : "已保存")}</span>
        </div>
        <div className="script-editor-actions">
          {failure && <span className="script-save-error" aria-live="polite">{failure}</span>}
          {(dirty || saving || Boolean(failure)) && <>
            <button type="button" className="secondary-button" disabled={saving} onClick={onCancel}><X size={15} />{locked ? "刷新当前版本" : "撤销修改"}</button>
            <button type="button" className="primary-button" disabled={!dirty || saving} onClick={onSave}><Save size={15} />{saving ? "正在保存…" : "保存新版本"}</button>
          </>}
        </div>
      </div>
      <div className={`script-editor-page ${pageClassName}`.trim()}>
        {header}
        <div
          ref={editorRef}
          id={editorID}
          className={`script-body-editor ${bodyClassName}`.trim()}
          role="textbox"
          aria-label={editorLabel}
          aria-multiline="true"
          contentEditable={!saving && !locked}
          aria-readonly={saving || locked}
          suppressContentEditableWarning
          spellCheck={false}
          onInput={commit}
          onMouseUp={() => editorRef.current && onSelection(editorRef.current)}
          onKeyUp={() => editorRef.current && onSelection(editorRef.current)}
        />
      </div>
    </div>
  );
}

function editorText(editor: HTMLDivElement) {
  return (editor.innerText || editor.textContent || "").replace(/\u00a0/g, " ").replace(/\n{3,}/g, "\n\n").trimEnd();
}

function queryCommand(command: FormatCommand) {
  try { return document.queryCommandState(command); }
  catch { return false; }
}
