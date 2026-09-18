package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agenttool"
)

func TestMCPToolOutputProvenanceExactBytesAndReopen(t *testing.T) {
	for _, toolID := range []string{"mcp:fixture/read_fact", "mcp:fixture/save_fact"} {
		t.Run(toolID, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "runtime.db")
			registry := loadTestRegistry(t)
			store, err := Open(path, registry)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { store.Close() }()
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			project, err := store.CreateProject(ctx, "MCP output")
			if err != nil {
				t.Fatal(err)
			}
			call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
				SDKToolCallID: "sdk-output", ToolID: toolID, Arguments: json.RawMessage(`{"key":"output"}`), ConfigurationHash: toolConfigurationHashForTest(t, store, toolID)})
			if err != nil {
				t.Fatal(err)
			}
			if call.Approval != nil {
				_, err = store.StoreAgentToolOutput(ctx, call.AgentToolCallID, call.SDKToolCallID, "pending.txt", []byte("42"))
				assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
				_, err = store.ResolveAgentToolApproval(ctx, ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID,
					ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve", ActorRef: "test_user"})
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err = store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID,
				ConfigurationHash: toolConfigurationHashForTest(t, store, toolID)})
			if err != nil {
				t.Fatal(err)
			}
			png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9Zl1sAAAAASUVORK5CYII=")
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range []struct {
				name string
				data []byte
			}{{"result.csv", []byte("value\n42\n")}, {"image.png", png}} {
				first, err := store.StoreAgentToolOutput(ctx, call.AgentToolCallID, call.SDKToolCallID, item.name, item.data)
				if err != nil {
					t.Fatal(err)
				}
				if first.Asset.SourceType != "mcp_tool" || !bytes.Contains(first.Asset.Metadata, []byte(call.AgentToolCallID)) || !bytes.Contains(first.Asset.Metadata, []byte(call.SDKToolCallID)) {
					t.Fatalf("MCP output provenance: %+v", first.Asset)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store, err = Open(path, registry)
				if err != nil {
					t.Fatal(err)
				}
				store.SetAgentToolRegistry(agentToolRegistryForTest(t))
				second, err := store.StoreAgentToolOutput(ctx, call.AgentToolCallID, call.SDKToolCallID, item.name, item.data)
				if err != nil || second.Asset.AssetID != first.Asset.AssetID || second.Snapshot.AssetSnapshotID != first.Snapshot.AssetSnapshotID {
					t.Fatalf("duplicate after reopen: %+v %v", second, err)
				}
				opened, err := store.OpenAssetContent(ctx, first.Asset.AssetID)
				if err != nil {
					t.Fatal(err)
				}
				actual, err := io.ReadAll(opened.File)
				opened.File.Close()
				if err != nil || !bytes.Equal(actual, item.data) {
					t.Fatalf("output bytes changed: %v", err)
				}
				_, err = store.OpenAssetContent(WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: "foreign", AgentTurnID: "other"}), first.Asset.AssetID)
				assertDomainCode(t, err, "ASSET_NOT_FOUND")
			}
			_, err = store.StoreAgentToolOutput(ctx, call.AgentToolCallID, "wrong", "result.csv", []byte("42"))
			assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
			_, err = store.CancelAgentToolCall(ctx, CancelAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Reason: "cancelled"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.StoreAgentToolOutput(ctx, call.AgentToolCallID, call.SDKToolCallID, "late.txt", []byte("42"))
			assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
		})
	}
}

func TestHostedToolOutputApprovalIdempotencyProvenanceAndCancellation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "runtime.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion, HostedTools: []agenttool.HostedToolConfig{{ID: "code", Type: "code_interpreter", Description: "Compute", Enabled: true, Access: agenttool.AccessSensitive}}})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(registry)
	project, err := store.CreateProject(ctx, "Hosted output")
	if err != nil {
		t.Fatal(err)
	}
	call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, SDKToolCallID: "sdk_output", ToolID: "hosted:code", Arguments: json.RawMessage(`{"instruction":"compute"}`), ConfigurationHash: toolConfigurationHashForTest(t, store, "hosted:code")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.StoreAgentToolOutput(ctx, call.AgentToolCallID, call.SDKToolCallID, "answer.txt", []byte("42"))
	assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
	if call.Approval == nil || !strings.Contains(call.Approval.Reason, "input_assets") {
		t.Fatal("Hosted approval must disclose file content transmission")
	}
	_, err = store.ResolveAgentToolApproval(ctx, ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID, ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve", ActorRef: "test_user"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID, ConfigurationHash: toolConfigurationHashForTest(t, store, call.ToolID)})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.StoreAgentToolOutput(ctx, call.AgentToolCallID, call.SDKToolCallID, "answer.txt", []byte("42"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.StoreAgentToolOutput(ctx, call.AgentToolCallID, call.SDKToolCallID, "answer.txt", []byte("42"))
	if err != nil || first.Asset.AssetID != second.Asset.AssetID {
		t.Fatalf("duplicate output: %+v, %v", second, err)
	}
	if first.Asset.SourceType != "hosted_tool" || !bytes.Contains(first.Asset.Metadata, []byte(call.AgentToolCallID)) {
		t.Fatalf("missing provenance: %+v", first.Asset)
	}
	for filename, body := range map[string]string{"table.csv": "value\n42\n", "data.json": `{"value":42}`} {
		output, err := store.StoreAgentToolOutput(ctx, call.AgentToolCallID, call.SDKToolCallID, filename, []byte(body))
		if err != nil || output.Asset.Kind != "text" || output.Asset.OriginalFilename != filename {
			t.Fatalf("text output: %+v %v", output, err)
		}
		text, err := store.GetParsedAssetText(ctx, output.Asset.AssetID, output.Snapshot.AssetSnapshotID)
		if err != nil || text != body {
			t.Fatalf("reusable text: %q %v", text, err)
		}
		_, err = store.OpenAssetContent(WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: "foreign", AgentTurnID: "turn"}), output.Asset.AssetID)
		assertDomainCode(t, err, "ASSET_NOT_FOUND")
	}
	archive, err := store.StoreAgentToolOutput(ctx, call.AgentToolCallID, call.SDKToolCallID, "output.bin", []byte("value\n42\n"))
	if err != nil {
		t.Fatal(err)
	}
	if archive.Asset.ExpiresAt != nil || archive.Asset.OriginalFilename != "output.bin.zip" {
		t.Fatalf("output archive: %+v", archive.Asset)
	}
	opened, err := store.OpenAssetContent(ctx, archive.Asset.AssetID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(opened.File)
	opened.File.Close()
	if err != nil {
		t.Fatal(err)
	}
	zipped, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zipped.File) != 1 || zipped.File[0].Name != "output.bin" {
		t.Fatalf("generated archive: %+v, %v", zipped, err)
	}
	_, err = store.StoreAgentToolOutput(ctx, call.AgentToolCallID, "wrong", "answer.txt", []byte("42"))
	assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
	for _, name := range []string{"../answer.txt", `C:\answer.txt`, "sub/answer.txt"} {
		_, err = store.StoreAgentToolOutput(ctx, call.AgentToolCallID, call.SDKToolCallID, name, []byte("42"))
		assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	}
	_, err = store.CancelAgentToolCall(ctx, CancelAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Reason: "cancelled"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.StoreAgentToolOutput(ctx, call.AgentToolCallID, call.SDKToolCallID, "late.txt", []byte("42"))
	assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
}
