package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestCommandRequestHashRetainsTheExistingTransportProtocol(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/original/messages", nil)
	hash, err := commandRequestHash(request, "project", agentcontract.MessageRequest{Content: "Original"})
	if err != nil || hash != "25dfb86b79f1bea257140f54318bc207e71e3f3b47c7eda26861ff24370dc7a0" {
		t.Fatalf("existing command hash protocol changed: %s %v", hash, err)
	}
}

func TestMessageSubmissionHashSeparatesClientIdentityFromLegacyBinding(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, change := range []string{"none", "content", "display", "skill", "version", "material", "view", "client", "project", "conversation", "method"} {
			t.Run(strconv.FormatBool(legacy)+"/"+change, func(t *testing.T) {
				original := agentcontract.MessageRequest{Content: "Original message", CapabilityRef: &agentcontract.CapabilityRef{CapabilityID: "original-skill", Version: "1.0.0", SelectionMode: "explicit"}}
				bound := original
				bound.AttachmentRefs = []agentcontract.AttachmentRef{{AssetID: "original-asset", AssetSnapshotID: "original-snapshot"}}
				request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/original/messages", nil)
				request.Header.Set("X-Client-Instance-ID", "original-client")
				project := "original-project"
				hashed := original
				if legacy {
					hashed = bound
				}
				hash, err := commandRequestHash(request, project, hashed)
				if err != nil {
					t.Fatal(err)
				}
				if !legacy {
					hash = messageRequestHashPrefix + hash
				}
				prior := businessruntime.AgentTurnSubmission{RequestHash: hash, Request: bound, Turn: businessruntime.AgentTurn{Request: agentcontract.MessageRequest{Content: "Later queue edit"}}}
				switch change {
				case "content":
					original.Content = "Different"
				case "display":
					original.DisplayContent = "Different"
				case "skill":
					original.CapabilityRef = &agentcontract.CapabilityRef{CapabilityID: "different", Version: "1.0.0"}
				case "version":
					original.CapabilityRef = &agentcontract.CapabilityRef{CapabilityID: "original-skill", Version: "2.0.0"}
				case "material":
					original.AttachmentRefs = []agentcontract.AttachmentRef{{AssetID: "different", AssetSnapshotID: "different"}}
				case "view":
					original.ClientContext.CurrentView = "Different"
				case "client":
					request.Header.Set("X-Client-Instance-ID", "different-client")
				case "project":
					project = "different-project"
				case "conversation":
					request.URL.Path = "/api/v1/conversations/different/messages"
				case "method":
					request.Method = http.MethodPut
				}
				incoming, err := commandRequestHash(request, project, original)
				if err != nil {
					t.Fatal(err)
				}
				matches, err := messageSubmissionMatches(request, project, original, incoming, prior)
				if err != nil || matches != (change == "none") {
					t.Fatalf("legacy=%t change=%s matches=%t error=%v", legacy, change, matches, err)
				}
			})
		}
	}
}

func TestMessageSubmissionDoesNotDowngradeNewHashOrInventLegacyAttachmentMetadata(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/original/messages", nil)
	for _, scenario := range []string{"new-explicit", "legacy-display", "legacy-hidden", "legacy-container", "legacy-multiple", "legacy-general"} {
		t.Run(scenario, func(t *testing.T) {
			original := agentcontract.MessageRequest{Content: "Original", CapabilityRef: &agentcontract.CapabilityRef{CapabilityID: "skill", Version: "1.0.0"}, AttachmentRefs: []agentcontract.AttachmentRef{{AssetID: "asset", AssetSnapshotID: "snapshot"}}}
			switch scenario {
			case "legacy-display":
				original.AttachmentRefs[0].DisplayName = "Explicit name"
			case "legacy-hidden":
				original.AttachmentRefs[0].Hidden = true
			case "legacy-container":
				container := "container"
				original.AttachmentRefs[0].ContainerAssetID = &container
			case "legacy-multiple":
				original.AttachmentRefs = append(original.AttachmentRefs, agentcontract.AttachmentRef{AssetID: "second", AssetSnapshotID: "second"})
			case "legacy-general":
				original.CapabilityRef = nil
			}
			hash, err := commandRequestHash(request, "project", original)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "new-explicit" {
				hash = messageRequestHashPrefix + hash
			}
			prior := businessruntime.AgentTurnSubmission{RequestHash: hash, Request: original}
			incoming := original
			incoming.AttachmentRefs = nil
			incomingHash, err := commandRequestHash(request, "project", incoming)
			if err != nil {
				t.Fatal(err)
			}
			if matches, err := messageSubmissionMatches(request, "project", incoming, incomingHash, prior); err != nil || matches {
				t.Fatalf("missing explicit material treated as legacy binding: %t %v", matches, err)
			}
		})
	}
}

func TestMessageSubmissionHTTPFreezesAutomaticMaterialAndRecoversWithAdmissionDisabled(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(strconv.FormatBool(legacy), func(t *testing.T) {
			ctx := context.Background()
			registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
			if err != nil {
				t.Fatal(err)
			}
			database := filepath.Join(t.TempDir(), "http-submission.db")
			store, err := businessruntime.Open(database, registry)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { store.Close() }()
			project, err := store.CreateProject(ctx, "Automatic material recovery")
			if err != nil {
				t.Fatal(err)
			}
			upload := func(name, text string) businessruntime.AssetResult {
				t.Helper()
				session, err := store.CreateUploadSession(ctx, project.ProjectID, []businessruntime.UploadItemSpec{{ClientItemKey: name, Kind: "text", OriginalFilename: name, DeclaredMIMEType: "text/plain", DeclaredSizeBytes: int64(len(text))}})
				if err != nil {
					t.Fatal(err)
				}
				item, err := store.WriteUploadContent(ctx, session.Items[0].UploadItemID, strings.NewReader(text))
				if err != nil {
					t.Fatal(err)
				}
				result, err := store.CompleteUploadItem(ctx, item.UploadItemID)
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			asset := upload("first.txt", "The original source.")
			entry, ok := registry.Get("novel_to_script")
			if !ok || entry.Definition == nil {
				t.Fatal("missing original workflow fixture")
			}
			body := agentcontract.MessageRequest{Content: "Adapt the source", CapabilityRef: &agentcontract.CapabilityRef{CapabilityID: "novel_to_script", Version: entry.Definition.Version, SelectionMode: "explicit"}}
			path := "/api/v1/conversations/" + project.PrimaryConversationID + "/messages"
			headers := map[string]string{"Idempotency-Key": "11111111-1111-4111-8111-111111111111", "X-Client-Instance-ID": "original-client"}
			server := NewWithRuntime(shell.New(registry), store, nil)
			// Exercise HTTP acceptance and durable storage without running a model worker.
			server.agentTurns = newAgentTurnManager(store, nil, slog.Default())
			if legacy {
				bound := body
				bound.AttachmentRefs = []agentcontract.AttachmentRef{{AssetID: asset.Asset.AssetID, AssetSnapshotID: asset.Snapshot.AssetSnapshotID}}
				request := httptest.NewRequest(http.MethodPost, path, nil)
				request.Header.Set("X-Client-Instance-ID", headers["X-Client-Instance-ID"])
				hash, err := commandRequestHash(request, project.ProjectID, bound)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, bound, businessruntime.CommandMeta{Scope: project.ProjectID, CommandType: "create_message", IdempotencyKey: headers["Idempotency-Key"], RequestHash: hash}); err != nil {
					t.Fatal(err)
				}
			}
			first := objectAt(t, performJSONWithHeaders(t, server.Handler(), http.MethodPost, path, body, headers, http.StatusAccepted), "data")
			turnID := stringAt(t, first, "agent_turn_id")
			refs := arrayAt(t, objectAt(t, first, "request"), "attachment_refs")
			if len(refs) != 1 || refs[0].(map[string]any)["asset_id"] != asset.Asset.AssetID {
				t.Fatalf("first message did not bind the original material: %+v", refs)
			}
			select {
			case <-server.agentTurns.wake:
				if legacy {
					t.Fatal("legacy receipt lookup notified the manager")
				}
			default:
				if !legacy {
					t.Fatal("new acceptance did not notify the manager")
				}
			}
			upload("second.txt", "Another source must not change the original request.")
			resolved, err := server.resolveSingleDurableCapabilityMaterial(ctx, project.ProjectID, body.CapabilityRef.CapabilityID)
			if err != nil || len(resolved) != 0 {
				t.Fatalf("fixture did not change current automatic binding: %+v %v", resolved, err)
			}
			current, err := store.UpdateQueuedAgentTurn(ctx, businessruntime.UpdateQueuedAgentTurnCommand{AgentTurnID: turnID, ExpectedContent: body.Content, Content: "Later queue edit"})
			if err != nil {
				t.Fatal(err)
			}
			repeated := objectAt(t, performJSONWithHeaders(t, server.Handler(), http.MethodPost, path, body, headers, http.StatusAccepted), "data")
			if repeated["agent_turn_id"] != turnID || objectAt(t, repeated, "request")["content"] != current.Request.Content || len(server.agentTurns.wake) != 0 {
				t.Fatalf("receipt recovery changed or dispatched the queued request: %+v", repeated)
			}
			_, receipt, err := store.LookupAgentTurnSubmission(ctx, project.PrimaryConversationID, headers["Idempotency-Key"])
			if err != nil || receipt == nil || strings.HasPrefix(receipt.RequestHash, messageRequestHashPrefix) != !legacy || receipt.Request.Content != body.Content {
				t.Fatalf("acceptance did not preserve its protocol and original request: %+v %v", receipt, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = businessruntime.Open(database, registry)
			if err != nil {
				t.Fatal(err)
			}
			server = NewWithRuntime(shell.New(registry), store, nil)
			server.ConfigureAgentRollout(NewAgentRolloutPolicy(false, nil, "recovery-only"))
			repeated = objectAt(t, performJSONWithHeaders(t, server.Handler(), http.MethodPost, path, body, headers, http.StatusAccepted), "data")
			if repeated["agent_turn_id"] != turnID || repeated["status"] != "accepted" || objectAt(t, repeated, "request")["content"] != "Later queue edit" {
				t.Fatalf("rebuild lost the original receipt: %+v", repeated)
			}
			changedHeaders := map[string]string{"Idempotency-Key": headers["Idempotency-Key"], "X-Client-Instance-ID": "different-client"}
			conflict := performJSONWithHeaders(t, server.Handler(), http.MethodPost, path, body, changedHeaders, http.StatusBadRequest)
			if objectAt(t, conflict, "error")["code"] != "IDEMPOTENCY_KEY_REUSED" {
				t.Fatalf("changed client escaped identity validation: %+v", conflict)
			}
			changedHeaders["Idempotency-Key"] = "22222222-2222-4222-8222-222222222222"
			performJSONWithHeaders(t, server.Handler(), http.MethodPost, path, body, changedHeaders, http.StatusServiceUnavailable)
			turns, err := store.ListProjectAgentTurns(ctx, project.ProjectID, 10)
			if err != nil || len(turns) != 1 || len(turns[0].Request.AttachmentRefs) != 1 || turns[0].Request.AttachmentRefs[0].AssetID != asset.Asset.AssetID {
				t.Fatalf("recovery changed the accepted material or created another turn: %+v %v", turns, err)
			}
		})
	}
}

func TestMessageSubmissionHTTPLegacyAndNewReceiptsSurviveSkillDisable(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(strconv.FormatBool(legacy), func(t *testing.T) {
			store, handler := openBackgroundPauseHTTP(t, filepath.Join(t.TempDir(), "skill-recovery.db"))
			defer store.Close()
			ctx := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: "owner", WorkspaceID: "pause", Role: identity.RoleOwner})
			project, err := store.CreateProject(ctx, "Skill acceptance recovery")
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/conversations/" + project.PrimaryConversationID + "/messages"
			body := agentcontract.MessageRequest{Content: "Research", CapabilityRef: &agentcontract.CapabilityRef{CapabilityID: "story_research_digest", Version: "1.0.0", SelectionMode: "explicit"}}
			request := httptest.NewRequest(http.MethodPost, path, nil)
			request.Header.Set("X-Client-Instance-ID", "original-client")
			hash, err := commandRequestHash(request, project.ProjectID, body)
			if err != nil {
				t.Fatal(err)
			}
			if !legacy {
				hash = messageRequestHashPrefix + hash
			}
			key := "11111111-1111-4111-8111-111111111111"
			turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, body, businessruntime.CommandMeta{Scope: project.ProjectID, CommandType: "create_message", IdempotencyKey: key, RequestHash: hash})
			if err != nil {
				t.Fatal(err)
			}
			installations, err := store.ListSkillInstallations(ctx, false)
			if err != nil {
				t.Fatal(err)
			}
			disabled := false
			for _, installation := range installations {
				if installation.CapabilityID == body.CapabilityRef.CapabilityID {
					if _, err := store.SetSkillInstallationEnabled(ctx, installation.SkillInstallationID, false, "user:owner"); err != nil {
						t.Fatal(err)
					}
					disabled = true
				}
			}
			if !disabled {
				t.Fatal("fixture did not disable the original Skill")
			}
			if _, err := store.PreflightMessage(ctx, project.PrimaryConversationID, body); err == nil {
				t.Fatal("fixture did not make the original Skill unavailable to a new request")
			}
			headers := bearer("pause-owner", key)
			headers["X-Client-Instance-ID"] = "original-client"
			repeated := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, path, body, headers, http.StatusAccepted), "data")
			if repeated["agent_turn_id"] != turn.AgentTurnID {
				t.Fatalf("disabled Skill prevented original acceptance recovery: %+v", repeated)
			}
			performJSONWithHeaders(t, handler, http.MethodPost, path, body, bearer("pause-viewer", key), http.StatusForbidden)
			performJSONWithHeaders(t, handler, http.MethodPost, path, body, bearer("pause-foreign", key), http.StatusNotFound)
			performJSONWithHeaders(t, handler, http.MethodPost, path, body, nil, http.StatusUnauthorized)
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
			request.Header.Set("Authorization", "Bearer pause-owner")
			request.Header.Set("Idempotency-Key", "22222222-2222-4222-8222-222222222222")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code < 400 {
				t.Fatalf("disabled Skill allowed fresh acceptance: %d %s", response.Code, response.Body.String())
			}
			turns, err := store.ListProjectAgentTurns(ctx, project.ProjectID, 10)
			if err != nil || len(turns) != 1 {
				t.Fatalf("recovery created another turn: %+v %v", turns, err)
			}
		})
	}
}
