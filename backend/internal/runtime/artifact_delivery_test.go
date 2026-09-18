package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func createDeliveryArtifact(t *testing.T, store *Store, project Project, kind, payload string) Artifact {
	t.Helper()
	artifact, err := store.CreateGenericArtifact(context.Background(), CreateGenericArtifactCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, IdempotencyKey: store.newID("delivery"), CommandType: "create_generic_artifact", RequestHash: "fixture"},
		ProjectID:   project.ProjectID, ConversationID: project.PrimaryConversationID,
		Draft: agentcontract.ArtifactDraft{ArtifactType: kind, Title: "Download fixture", Payload: json.RawMessage(payload)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func TestArtifactDeliveryUsesPayloadContractWithoutCapabilityTypeBranches(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "delivery.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Plugin delivery")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, payload, text string
		formats             []string
	}{
		{"plugin_script", `{"title":"Scene","script_text":"Scene 1\nSpeaker: Ready."}`, "Scene 1\nSpeaker: Ready.", []string{"json", "txt", "md", "docx"}},
		{"plugin_document", `{"content_markdown":"# Saved body"}`, "# Saved body", []string{"json", "txt", "md", "docx"}},
		{"plugin_table", `{"columns":["value"],"rows":[[9007199254740993]]}`, "", []string{"json", "csv"}},
		{"plugin_mixed", `{"content":"Table notes","columns":["value"],"rows":[[1]]}`, "Table notes", []string{"json", "txt", "md", "docx", "csv"}},
		{"plugin_ambiguous", `{"content":"Commentary","script_text":"Different body"}`, "", []string{"json"}},
		{"plugin_structured", `{"findings":[{"body":"Not a top-level document"}]}`, "", []string{"json"}},
		{"plugin_array", `[9007199254740993,{"script_text":"Nested only"}]`, "", []string{"json"}},
		{"plugin_scalar", `9007199254740993`, "", []string{"json"}},
		{"plugin_null", `null`, "", []string{"json"}},
		{"plugin_control", `{"script_text":"Body\u0001"}`, "Body\x01", []string{"json", "txt", "md"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Isolated stored-output fixtures, not evidence of a real Skill installation.
			artifact := createDeliveryArtifact(t, store, project, "generic_document", `{"content":"seed"}`)
			if _, err := store.db.Exec(`UPDATE artifacts SET artifact_type=? WHERE artifact_id=?`, test.name, artifact.ArtifactID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE artifact_versions SET payload_json=? WHERE artifact_version_id=?`, test.payload, artifact.CurrentVersionID); err != nil {
				t.Fatal(err)
			}
			delivery, err := store.GetArtifactDelivery(ctx, artifact.CurrentVersionID)
			if err != nil {
				t.Fatal(err)
			}
			var formats []string
			for _, download := range delivery.Downloads {
				formats = append(formats, download.Format)
			}
			if !reflect.DeepEqual(formats, test.formats) {
				t.Fatalf("formats: %v, want %v", formats, test.formats)
			}
			_, data, err := store.DownloadArtifactVersion(ctx, artifact.CurrentVersionID, "json")
			if err != nil || string(data) != test.payload {
				t.Fatalf("unfaithful JSON: %s %v", data, err)
			}
			_, data, err = store.DownloadArtifactVersion(ctx, artifact.CurrentVersionID, "txt")
			if test.text != "" {
				if err != nil || string(data) != test.text {
					t.Fatalf("text body: %q %v", data, err)
				}
			} else {
				assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
			}
		})
	}
}

func TestArtifactDeliveryImmutableDocumentFormatsScopeAndRestart(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "delivery.db")
	store, err := Open(database, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	project, err := store.CreateProject(ctx, "Download fixture")
	if err != nil {
		t.Fatal(err)
	}
	content := "# \u4e2d\u6587 <&>\n\nHello\tworld\n"
	payload, _ := json.Marshal(map[string]any{"title": "../CON:\r\n\u4e2d\u6587", "content_markdown": content, "content": "not the displayed field", "metadata": map[string]any{"large_number": json.Number("9007199254740993")}})
	artifact := createDeliveryArtifact(t, store, project, "generic_document", string(payload))
	versionID := artifact.CurrentVersionID
	delivery, err := store.GetArtifactDelivery(ctx, versionID)
	if err != nil || len(delivery.Downloads) != 4 || delivery.Version != 1 || delivery.Status != "confirmed" {
		t.Fatalf("delivery: %+v, %v", delivery, err)
	}
	for _, format := range []string{"txt", "md"} {
		download, data, err := store.DownloadArtifactVersion(ctx, versionID, format)
		if err != nil || string(data) != content || strings.ContainsAny(download.Filename, "/\\:\r\n") || !strings.Contains(download.DownloadURL, versionID) {
			t.Fatalf("%s: %+v %q %v", format, download, data, err)
		}
	}
	_, jsonData, err := store.DownloadArtifactVersion(ctx, versionID, "json")
	version, _ := store.GetArtifactVersion(ctx, versionID)
	if err != nil || !bytes.Equal(jsonData, version.Payload) || !bytes.Contains(jsonData, []byte("9007199254740993")) {
		t.Fatalf("JSON is not faithful: %s %v", jsonData, err)
	}
	_, docx, err := store.DownloadArtifactVersion(ctx, versionID, "docx")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(docx), int64(len(docx)))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range archive.File {
		if file.Name != "word/document.xml" {
			continue
		}
		found = true
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		decoder := xml.NewDecoder(reader)
		var text strings.Builder
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if chars, ok := token.(xml.CharData); ok {
				text.Write(chars)
			}
		}
		reader.Close()
		if !strings.Contains(text.String(), "\u4e2d\u6587 <&>") {
			t.Fatalf("DOCX lost text: %s", text.String())
		}
	}
	if !found {
		t.Fatal("DOCX body missing")
	}
	_, err = store.CreateConfirmedGenericArtifactVersion(ctx, CreateVersionCommand{ArtifactID: artifact.ArtifactID, BaseVersionID: versionID, BaseVersion: 1, ChangeMode: "manual_edit", NewPayload: json.RawMessage(`{"title":"New title","content":"new body"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(database, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	old, bytesAgain, err := store.DownloadArtifactVersion(ctx, versionID, "txt")
	if err != nil || string(bytesAgain) != content || !strings.Contains(old.Filename, "v1-") || strings.Contains(old.Filename, "New title") {
		t.Fatalf("old download changed: %+v %q %v", old, bytesAgain, err)
	}
	viewer := identity.DefaultLocalPrincipal()
	viewer.Role = identity.RoleViewer
	if _, err := store.GetArtifactDelivery(identity.WithPrincipal(ctx, viewer), versionID); err != nil {
		t.Fatal(err)
	}
	foreign := viewer
	foreign.WorkspaceID = "other-workspace"
	for _, denied := range []context.Context{identity.WithPrincipal(ctx, foreign), WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: "other-project", AgentTurnID: "turn"})} {
		_, err := store.GetArtifactDelivery(denied, versionID)
		assertDomainCode(t, err, "PROJECT_NOT_FOUND")
		_, _, err = store.DownloadArtifactVersion(denied, versionID, "json")
		assertDomainCode(t, err, "PROJECT_NOT_FOUND")
	}
	for _, format := range []string{"", "pdf", "../../txt", "TXT"} {
		_, _, err := store.DownloadArtifactVersion(ctx, versionID, format)
		assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	}
	if _, err := store.db.Exec(`UPDATE projects SET deleted_at = ? WHERE project_id = ?`, formatTime(store.now()), project.ProjectID); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.DownloadArtifactVersion(ctx, versionID, "txt")
	assertDomainCode(t, err, "PROJECT_NOT_FOUND")
}

func TestArtifactDeliveryTablePreservesOrderPrecisionAndQuotesFormulaText(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "delivery.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, err := store.CreateProject(context.Background(), "Table")
	if err != nil {
		t.Fatal(err)
	}
	artifact := createDeliveryArtifact(t, store, project, "generic_table", `{"columns":[{"key":"z","label":"=unsafe"},"a","b"],"rows":[["\u4e2d\u6587,quoted",9007199254740993,true],{"a":"  @SUM(1)","b":null,"z":"line\nnext"}]}`)
	delivery, err := store.GetArtifactDelivery(context.Background(), artifact.CurrentVersionID)
	if err != nil || len(delivery.Downloads) != 2 || len(delivery.Warnings) != 1 {
		t.Fatalf("delivery: %+v %v", delivery, err)
	}
	_, data, err := store.DownloadArtifactVersion(context.Background(), artifact.CurrentVersionID, "csv")
	if err != nil || !bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		t.Fatalf("csv: %q %v", data, err)
	}
	records, err := csv.NewReader(bytes.NewReader(data[3:])).ReadAll()
	want := [][]string{{"'=unsafe", "a", "b"}, {"\u4e2d\u6587,quoted", "9007199254740993", "true"}, {"'line\nnext", "'  @SUM(1)", ""}}
	if err != nil || !reflect.DeepEqual(records, want) {
		t.Fatalf("records: %#v %v", records, err)
	}
	for _, raw := range []string{"=1", "+cmd", "-2", "@SUM(1)", "\t=1", "\u200b=1", "\uff1d1", " \rtext"} {
		if artifactCSVText(raw) != "'"+raw {
			t.Errorf("unsafe CSV value %q", raw)
		}
	}
}

func TestArtifactDeliveryIncompleteContentOnlyOffersFaithfulJSON(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "delivery.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, err := store.CreateProject(context.Background(), "Invalid tables")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, kind, payload string }{
		{"document-without-body", "generic_document", `{"notes":["not a body"]}`},
		{"no-columns", "generic_table", `{"rows":[[1]]}`},
		{"ragged", "generic_table", `{"columns":["a"],"rows":[[1,2]]}`},
		{"unknown-field", "generic_table", `{"columns":["a"],"rows":[{"a":1,"hidden":2}]}`},
		{"nested", "generic_table", `{"columns":["a"],"rows":[[{"data":1}]]}`},
		{"duplicate", "generic_table", `{"columns":["a","a"],"rows":[]}`},
		{"bad-row", "generic_table", `{"columns":["a"],"rows":["text"]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact := createDeliveryArtifact(t, store, project, test.kind, test.payload)
			delivery, err := store.GetArtifactDelivery(context.Background(), artifact.CurrentVersionID)
			if err != nil || len(delivery.Downloads) != 1 || delivery.Downloads[0].Format != "json" || len(delivery.Warnings) != 1 {
				t.Fatalf("delivery: %+v %v", delivery, err)
			}
			_, _, err = store.DownloadArtifactVersion(context.Background(), artifact.CurrentVersionID, "csv")
			assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
		})
	}
	artifact := createDeliveryArtifact(t, store, project, "generic_document", `{"content":"control\u0001"}`)
	delivery, err := store.GetArtifactDelivery(context.Background(), artifact.CurrentVersionID)
	if err != nil || len(delivery.Downloads) != 3 || len(delivery.Warnings) != 1 {
		t.Fatalf("invalid XML: %+v %v", delivery, err)
	}
	_, _, err = store.DownloadArtifactVersion(context.Background(), artifact.CurrentVersionID, "docx")
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
}
