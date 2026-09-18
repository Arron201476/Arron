export type ResizablePane = "sidebar" | "agent";

export const DEFAULT_PANE_WIDTHS: Record<ResizablePane, number> = {
  sidebar: 224,
  agent: 360,
};

export const PANE_WIDTH_LIMITS: Record<ResizablePane, { min: number; max: number }> = {
  sidebar: { min: 200, max: 360 },
  agent: { min: 320, max: 560 },
};

export function clampPaneWidth(pane: ResizablePane, width: number) {
  const limits = PANE_WIDTH_LIMITS[pane];
  if (!Number.isFinite(width)) return DEFAULT_PANE_WIDTHS[pane];
  return Math.min(limits.max, Math.max(limits.min, Math.round(width)));
}

export function parseStoredPaneWidth(pane: ResizablePane, value: string | null) {
  if (!value?.trim()) return DEFAULT_PANE_WIDTHS[pane];
  return clampPaneWidth(pane, Number(value));
}

export function adjustPaneWidth(pane: ResizablePane, width: number, delta: number) {
  return clampPaneWidth(pane, width + delta);
}
