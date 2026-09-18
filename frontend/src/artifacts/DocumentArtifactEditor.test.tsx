// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DocumentArtifactEditor } from "./DocumentArtifactEditor";

afterEach(cleanup);

describe("DocumentArtifactEditor", () => {
  it("edits the document title and body as one full document payload", () => {
    const onChange = vi.fn();
    const view = render(<DocumentArtifactEditor
      payload={{ title: "旧标题", content: "旧正文", metadata: "保留" }}
      dirty={false}
      saving={false}
      failure=""
      onChange={onChange}
      onSave={vi.fn()}
      onCancel={vi.fn()}
      selectionBase={{ artifactID: "artifact-1", artifactType: "generic_document", artifactLabel: "文档" }}
      onSelectionChange={vi.fn()}
    />);

    const body = view.getByLabelText("文档正文");
    expect(body.classList.contains("document-body-editor")).toBe(true);
    body.textContent = "扩写后的正文";
    fireEvent.input(body);
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ title: "旧标题", content: "扩写后的正文", metadata: "保留" }));
  });

  it("reads and preserves the generic document content_markdown contract", () => {
    const onChange = vi.fn();
    render(<DocumentArtifactEditor
      payload={{ title: "雾港来信", content_markdown: "# 标题\n\n完整正文", metadata: "保留" }}
      dirty={false}
      saving={false}
      failure=""
      onChange={onChange}
      onSave={vi.fn()}
      onCancel={vi.fn()}
      selectionBase={{ artifactID: "artifact-1", artifactType: "generic_document", artifactLabel: "文档" }}
      onSelectionChange={vi.fn()}
    />);

    const body = screen.getByLabelText("文档正文");
    expect(body.textContent).toContain("完整正文");
    body.textContent = "修订后的正文";
    fireEvent.input(body);
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({
      title: "雾港来信",
      content_markdown: "修订后的正文",
      metadata: "保留",
    }));
    expect(onChange.mock.calls.at(-1)?.[0]).not.toHaveProperty("content");
  });
});
