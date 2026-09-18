import { captureDOMSelection, type SelectionCaptureBase } from "../contextTargeting";
import type { AgentTargetSelection } from "../types";
import { TextArtifactEditor } from "./TextArtifactEditor";

type Props = {
  payload: unknown;
  dirty: boolean;
  saving: boolean;
  locked?: boolean;
  saveStatus?: string;
  failure: string;
  onChange: (payload: unknown) => void;
  onSave: () => void;
  onCancel: () => void;
  selectionBase: SelectionCaptureBase;
  onSelectionChange: (selection: AgentTargetSelection | null) => void;
};

export function DocumentArtifactEditor({ payload, dirty, saving, locked, saveStatus, failure, onChange, onSave, onCancel, selectionBase, onSelectionChange }: Props) {
  const record = asRecord(payload);
  const title = typeof record.title === "string" ? record.title : "";
  const contentField = typeof record.content_markdown === "string" ? "content_markdown" : "content";
  const content = typeof record[contentField] === "string" ? record[contentField] as string : "";

  return (
    <TextArtifactEditor editorID="document-body" editorLabel="文档正文" toolbarLabel="文档编辑工具栏"
      text={content} dirty={dirty} saving={saving} locked={locked} saveStatus={saveStatus} failure={failure}
      pageClassName="document-editor-page" bodyClassName="document-body-editor"
      header={<input className="document-title-editor" aria-label="文档标题" value={title} disabled={saving || locked} onChange={(event) => onChange({ ...record, title: event.currentTarget.value })} placeholder="文档标题" />}
      onCommit={(text, html) => onChange({ ...record, [contentField]: text, editor_html: html })}
      onSelection={(editor) => onSelectionChange(captureDOMSelection(editor, { ...selectionBase, fieldPath: contentField }))}
      onSave={onSave} onCancel={onCancel} />
  );
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}
