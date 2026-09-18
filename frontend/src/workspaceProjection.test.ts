import { describe, expect, it } from "vitest";
import {
  artifactPresentationsFromRegistries,
  capabilityDependenciesReady,
  resolveAgentToolInteraction,
  resolveApprovalViewKey,
  resolveTaskViewKey,
  trustedComposerViewKey,
  trustedConfigViewKey,
} from "./workspaceProjection";
import type { Approval, WorkspaceRegistries } from "./types";
import { dynamicStoryEntry } from "./testComposerFixtures";

describe("workspace projection trust boundary", () => {
  it("does not enable disabled or unavailable skills with no missing dependencies", () => {
    expect(capabilityDependenciesReady(dynamicStoryEntry)).toBe(true);
    for (const status of ["disabled", "unavailable", "pending_runtime_support"]) {
      expect(capabilityDependenciesReady({ ...dynamicStoryEntry, status })).toBe(false);
    }
  });

  it("routes unknown view keys to read-only inspectors", () => {
    expect(trustedComposerViewKey("remote_component_url")).toBe("inspector");
    expect(trustedConfigViewKey("execute_html")).toBe("inspector");

    const presentations = artifactPresentationsFromRegistries({
      ...emptyRegistries,
      artifacts: [{
        artifact_type: "future_artifact", view_key: "remote_component_url", label: "Future",
        description: "Unknown renderer", editable: true, preferred_fields: [], available_actions: ["manual_edit"],
      }],
      navigation: [{ artifact_type: "future_artifact", order: 10, group_mode: "single", visibility: "user" }],
    });
    expect(presentations[0]).toMatchObject({ renderer: "document", editable: false, available_actions: ["inspect"] });
  });

  it("selects approval views only through the ordered server registry", () => {
    const registry: WorkspaceRegistries["approvals"] = [
      { registry_key: "single", view_key: "single_option", match: { option: "select_single_option" } },
      { registry_key: "default", view_key: "action_list", match: {} },
    ];
    expect(resolveApprovalViewKey(registry, approval(["select_single_option"]))).toBe("single_option");
    expect(resolveApprovalViewKey(registry, approval(["approve"]))).toBe("action_list");
    expect(resolveApprovalViewKey([{ registry_key: "unsafe", view_key: "iframe", match: {} }], approval(["approve"]))).toBe("inspector");
  });

  it("selects task views only through the server registry", () => {
    const registry: WorkspaceRegistries["tasks"] = [
      { status: "running", view_key: "task_progress" },
      { status: "completed", view_key: "task_result" },
    ];
    expect(resolveTaskViewKey(registry, "running")).toBe("task_progress");
    expect(resolveTaskViewKey(registry, "completed")).toBe("task_result");
    expect(resolveTaskViewKey([{ status: "running", view_key: "remote_component_url" }], "running")).toBe("inspector");
    expect(resolveTaskViewKey(registry, "future_status")).toBe("inspector");
  });

  it("exposes only trusted Agent tool approval commands", () => {
    expect(resolveAgentToolInteraction([
      { registry_key: "agent_tool", view_key: "agent_tool_approval", commands: ["approve", "deny", "execute_javascript"] },
    ])).toEqual({ viewKey: "agent_tool_approval", commands: ["approve", "deny"] });
    expect(resolveAgentToolInteraction([
      { registry_key: "agent_tool", view_key: "remote_component_url", commands: ["approve"] },
    ])).toEqual({ viewKey: "inspector", commands: [] });
    expect(resolveAgentToolInteraction([])).toEqual({ viewKey: "inspector", commands: [] });
  });
});

const emptyRegistries: WorkspaceRegistries = {
  artifacts: [], navigation: [], tasks: [], approvals: [], interactions: [], composer: [],
};

function approval(options: string[]): Approval {
  return {
    approval_request_id: "approval-1", run_id: "run-1",
    subject_kind: "artifact", subject_ref_id: "artifact-version-1", subject_version: 1,
    subject_snapshot_hash: "hash", status: "pending", title: "Confirm", reason: "Review", options,
    scope: "artifact", version: 1, requested_at: "2026-09-04T00:00:00Z",
  };
}
