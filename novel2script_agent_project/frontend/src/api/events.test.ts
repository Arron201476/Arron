import { describe, expect, it } from "vitest";
import { mergeEvents } from "./events";

describe("mergeEvents", () => {
  it("deduplicates reconnect replay by event id", () => {
    const first = { event_id: "e1", run_id: "r1", type: "step_started", message: "old" };
    const updated = { ...first, message: "new" };
    expect(mergeEvents([first], [updated, { event_id: "e2", run_id: "r1", type: "step_completed", message: "done" }])).toEqual([updated, expect.objectContaining({ event_id: "e2" })]);
  });

  it("restores event order when a snapshot and live stream arrive out of order", () => {
    const first = { event_id: "event_000001", run_id: "r1", type: "step_started", message: "first", created_at: "2026-07-15T10:00:00Z" };
    const second = { event_id: "event_000002", run_id: "r1", type: "step_completed", message: "second", created_at: "2026-07-15T10:00:01Z" };
    expect(mergeEvents([second], [first]).map((event) => event.event_id)).toEqual(["event_000001", "event_000002"]);
  });
});
