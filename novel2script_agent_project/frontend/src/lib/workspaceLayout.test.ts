import { describe, expect, it } from "vitest";
import { adjustPaneWidth, DEFAULT_PANE_WIDTHS, parseStoredPaneWidth } from "./workspaceLayout";

describe("workspaceLayout", () => {
  it("uses defaults for missing or invalid stored widths", () => {
    expect(parseStoredPaneWidth("sidebar", null)).toBe(DEFAULT_PANE_WIDTHS.sidebar);
    expect(parseStoredPaneWidth("agent", "not-a-number")).toBe(DEFAULT_PANE_WIDTHS.agent);
  });

  it("clamps stored widths to the supported range", () => {
    expect(parseStoredPaneWidth("sidebar", "120")).toBe(200);
    expect(parseStoredPaneWidth("sidebar", "420")).toBe(360);
    expect(parseStoredPaneWidth("agent", "240")).toBe(320);
    expect(parseStoredPaneWidth("agent", "900")).toBe(560);
  });

  it("supports keyboard-sized adjustments without exceeding limits", () => {
    expect(adjustPaneWidth("sidebar", 224, 16)).toBe(240);
    expect(adjustPaneWidth("agent", 330, -24)).toBe(320);
  });
});
