package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/scriptsandbox"
)

const nativeWorkspaceArchiveBytes = 18 << 20

var nativeWorkspaceHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ConfigureNativeWorkspaceEngine is startup-only; it does not create an
// environment or enable any administrator policy by itself.
func (s *Server) ConfigureNativeWorkspaceEngine(engine businessruntime.NativeWorkspaceEngine) {
	s.nativeWorkspaceEngine = engine
}

func (s *Server) nativeWorkspaceRoutes() {
	s.mux.HandleFunc("GET /internal/v1/native-workspaces/current", s.currentNativeWorkspace)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/reservations", s.reserveNativeWorkspace)
	s.mux.HandleFunc("GET /internal/v1/native-workspaces/{session_id}/lease", s.validateNativeWorkspace)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/{session_id}/lease/renew", s.renewNativeWorkspace)
	s.mux.HandleFunc("GET /internal/v1/native-workspaces/{session_id}/recovery", s.nativeWorkspaceRecovery)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/{session_id}/publications", s.nativeWorkspacePublication)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/{session_id}/memory-publications", s.nativeMemoryPublication)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/{session_id}/pty", s.executeNativeWorkspacePTY)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/{session_id}/pty/terminate", s.terminateNativeWorkspacePTYs)
	s.mux.HandleFunc("GET /internal/v1/native-workspaces/{session_id}/pty/state", s.readNativeWorkspacePTYState)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/{session_id}/ensure", s.ensureNativeWorkspace)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/{session_id}/probe", s.probeNativeWorkspace)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/{session_id}/shutdown", s.shutdownNativeWorkspace)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/{session_id}/restore", s.restoreNativeWorkspace)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/{session_id}/export", s.exportNativeWorkspace)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/{session_id}/commands", s.executeNativeWorkspaceCommand)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/{session_id}/files", s.nativeWorkspaceFile)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/{session_id}/manifest", s.nativeWorkspaceManifest)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/{session_id}/manifest/file", s.nativeWorkspaceManifestFile)
	s.mux.HandleFunc("POST /internal/v1/native-workspaces/{session_id}/manifest/seal", s.sealNativeWorkspaceManifest)
	s.mux.HandleFunc("PUT /internal/v1/native-workspaces/{session_id}/snapshots/{request_id}", s.saveNativeSnapshot)
	s.mux.HandleFunc("GET /internal/v1/native-workspaces/{session_id}/snapshots/{version}", s.readNativeSnapshot)
	s.mux.HandleFunc("GET /internal/v1/native-workspaces/{session_id}/snapshot-receipts/{request_id}", s.readNativeSnapshotReceipt)
}

func (s *Server) nativeWorkspaceManifest(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	var body *businessruntime.NativeWorkspaceManifestSources
	if !decodeBodyWithLimit(writer, request, &body, 64<<10) {
		return
	}
	if body == nil {
		writeError(writer, http.StatusBadRequest, "NATIVE_WORKSPACE_MANIFEST_INVALID", "A source inventory object is required.")
		return
	}
	manager, ok := s.nativeWorkspaceManager(writer, request)
	if !ok {
		return
	}
	result, err := manager.PrepareManifest(request.Context(), access, *body)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) nativeWorkspaceRecovery(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	manager, ok := s.nativeWorkspaceManager(writer, request)
	if !ok {
		return
	}
	result, err := manager.ReadRecovery(request.Context(), access)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) nativeWorkspacePublication(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	var body *businessruntime.NativeWorkspacePublicationRequest
	if !decodeBodyWithLimit(writer, request, &body, 256<<10) {
		return
	}
	if body == nil {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "Publication requires its approved snapshot selection.")
		return
	}
	manager, ok := s.nativeWorkspaceManager(writer, request)
	if !ok {
		return
	}
	result, err := manager.PublishFiles(request.Context(), access, *body)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) nativeMemoryPublication(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	var body *businessruntime.AgentMemoryPublicationRequest
	if !decodeBodyWithLimit(writer, request, &body, 128<<10) {
		return
	}
	if body == nil {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "Memory publication requires its approved snapshot selection.")
		return
	}
	manager, ok := s.nativeWorkspaceManager(writer, request)
	if !ok {
		return
	}
	result, err := manager.PublishMemory(request.Context(), access, *body)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) nativeWorkspaceManifestFile(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	var body struct {
		ManifestHash string `json:"manifest_hash"`
		Path         string `json:"path"`
	}
	if !decodeBodyWithLimit(writer, request, &body, 4096) {
		return
	}
	manager, ok := s.nativeWorkspaceManager(writer, request)
	if !ok {
		return
	}
	data, err := manager.ReadManifestFile(request.Context(), access, body.ManifestHash, body.Path)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	if data == nil {
		data = []byte{}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"session_id": access.SessionID, "manifest_hash": body.ManifestHash, "path": body.Path, "content": data}})
}

func (s *Server) sealNativeWorkspaceManifest(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	var body struct {
		ManifestHash string `json:"manifest_hash"`
	}
	if !decodeBodyWithLimit(writer, request, &body, 4096) {
		return
	}
	manager, ok := s.nativeWorkspaceManager(writer, request)
	if !ok {
		return
	}
	if err := manager.SealManifest(request.Context(), access, body.ManifestHash); err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"session_id": access.SessionID, "manifest_hash": body.ManifestHash, "status": "completed"}})
}

func nativeWorkspaceIdentity(writer http.ResponseWriter, request *http.Request) (int64, bool) {
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if !authorizeInternalAgent(writer, request) {
		return 0, false
	}
	activity, ok := businessruntime.AgentActivityFromContext(request.Context())
	if !ok || activity.AllowTerminal {
		writeError(writer, http.StatusForbidden, "AGENT_ACTIVITY_REQUIRED", "A live internal execution identity is required.")
		return 0, false
	}
	dispatch, err := nativeHeaderInteger(request, "X-Agent-Dispatch-Generation", 0)
	if err != nil || activity.AgentTurnID != "" && dispatch < 1 || activity.AgentTurnID == "" && dispatch != 0 {
		writeError(writer, http.StatusBadRequest, "NATIVE_WORKSPACE_REQUEST_INVALID", "Execution dispatch generation is invalid.")
		return 0, false
	}
	return dispatch, true
}

func nativeHeaderInteger(request *http.Request, name string, minimum int64) (int64, error) {
	values := request.Header.Values(name)
	if len(values) != 1 {
		return 0, errors.New("one numeric header is required")
	}
	value, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || value < minimum || strconv.FormatInt(value, 10) != values[0] {
		return 0, errors.New("invalid numeric header")
	}
	return value, nil
}

func (s *Server) nativeWorkspaceAccess(writer http.ResponseWriter, request *http.Request) (businessruntime.NativeWorkspaceAccess, bool) {
	var access businessruntime.NativeWorkspaceAccess
	dispatch, ok := nativeWorkspaceIdentity(writer, request)
	if !ok {
		return access, false
	}
	key := request.Header.Get("X-Workspace-Lease-Key")
	generation, err := nativeHeaderInteger(request, "X-Workspace-Lease-Generation", 1)
	if len(request.Header.Values("X-Workspace-Lease-Key")) != 1 || !nativeWorkspaceHash.MatchString(key) || err != nil {
		writeError(writer, http.StatusBadRequest, "NATIVE_WORKSPACE_REQUEST_INVALID", "Workspace lease headers are invalid.")
		return access, false
	}
	access = businessruntime.NativeWorkspaceAccess{SessionID: request.PathValue("session_id"), Generation: generation, HolderKey: key, DispatchGeneration: dispatch}
	if _, err := s.runtime.ValidateNativeWorkspaceLease(request.Context(), access); err != nil {
		writeNativeWorkspaceError(writer, err)
		return access, false
	}
	return access, true
}

func (s *Server) currentNativeWorkspace(writer http.ResponseWriter, request *http.Request) {
	dispatch, ok := nativeWorkspaceIdentity(writer, request)
	if !ok {
		return
	}
	lease, err := s.runtime.CurrentNativeWorkspaceLease(request.Context(), dispatch)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"lease": lease}})
}

func (s *Server) reserveNativeWorkspace(writer http.ResponseWriter, request *http.Request) {
	dispatch, ok := nativeWorkspaceIdentity(writer, request)
	if !ok {
		return
	}
	key := request.Header.Get("X-Workspace-Lease-Key")
	if len(request.Header.Values("X-Workspace-Lease-Key")) != 1 || !nativeWorkspaceHash.MatchString(key) {
		writeError(writer, http.StatusBadRequest, "NATIVE_WORKSPACE_REQUEST_INVALID", "A random lease holder key is required.")
		return
	}
	var body struct {
		ExpectedGeneration int64 `json:"expected_generation"`
	}
	if !decodeBodyWithLimit(writer, request, &body, 4096) {
		return
	}
	lease, err := s.runtime.ReserveNativeWorkspace(request.Context(), businessruntime.ReserveNativeWorkspaceCommand{HolderKey: key, ExpectedGeneration: body.ExpectedGeneration, DispatchGeneration: dispatch})
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": lease})
}

func (s *Server) validateNativeWorkspace(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	lease, err := s.runtime.ValidateNativeWorkspaceLease(request.Context(), access)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": lease})
}

func (s *Server) nativeWorkspaceManager(writer http.ResponseWriter, request *http.Request) (*businessruntime.NativeWorkspaceManager, bool) {
	if s.nativeWorkspaceEngine == nil {
		writeError(writer, http.StatusServiceUnavailable, "NATIVE_WORKSPACE_UNAVAILABLE", "An isolated workspace engine has not been configured.")
		return nil, false
	}
	policy, err := s.runtime.GetScriptSandboxPolicy(request.Context())
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return nil, false
	}
	manager, err := businessruntime.NewPolicyNativeWorkspaceManager(s.runtime, s.nativeWorkspaceEngine, policy.Limits)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return nil, false
	}
	return manager, true
}

func (s *Server) renewNativeWorkspace(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	var body struct{}
	if !decodeBodyWithLimit(writer, request, &body, 4096) {
		return
	}
	lease, err := s.runtime.RenewNativeWorkspaceLease(request.Context(), access)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": lease})
}

func (s *Server) ensureNativeWorkspace(writer http.ResponseWriter, request *http.Request) {
	s.nativeWorkspaceLifecycle(writer, request, "ensure")
}

func (s *Server) probeNativeWorkspace(writer http.ResponseWriter, request *http.Request) {
	s.nativeWorkspaceLifecycle(writer, request, "probe")
}

func (s *Server) shutdownNativeWorkspace(writer http.ResponseWriter, request *http.Request) {
	s.nativeWorkspaceLifecycle(writer, request, "shutdown")
}

func (s *Server) nativeWorkspaceLifecycle(writer http.ResponseWriter, request *http.Request, operation string) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	var body struct{}
	if !decodeBodyWithLimit(writer, request, &body, 4096) {
		return
	}
	manager, ok := s.nativeWorkspaceManager(writer, request)
	if !ok {
		return
	}
	var result businessruntime.NativeWorkspaceEnvironment
	var err error
	switch operation {
	case "probe":
		result, err = manager.Probe(request.Context(), access)
	case "shutdown":
		result, err = manager.Shutdown(request.Context(), access)
	default:
		result, err = manager.Ensure(request.Context(), access)
	}
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) restoreNativeWorkspace(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	var body businessruntime.RestoreNativeWorkspaceCommand
	if !decodeBodyWithLimit(writer, request, &body, 4096) {
		return
	}
	manager, ok := s.nativeWorkspaceManager(writer, request)
	if !ok {
		return
	}
	result, err := manager.Restore(request.Context(), access, body)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) exportNativeWorkspace(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	var body struct{}
	if !decodeBodyWithLimit(writer, request, &body, 4096) {
		return
	}
	manager, ok := s.nativeWorkspaceManager(writer, request)
	if !ok {
		return
	}
	result, err := manager.Export(request.Context(), access)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeNativeArchive(writer, access.SessionID, 0, result)
}

func (s *Server) saveNativeSnapshot(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	parent, err := nativeHeaderInteger(request, "X-Workspace-Snapshot-Parent", 0)
	hash := request.Header.Get("X-Workspace-Snapshot-Sha256")
	contentType, _, typeErr := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || parent > 128 || len(request.Header.Values("X-Workspace-Snapshot-Sha256")) != 1 || !nativeWorkspaceHash.MatchString(hash) {
		writeError(writer, http.StatusBadRequest, "NATIVE_WORKSPACE_REQUEST_INVALID", "Snapshot parent and SHA-256 headers are required.")
		return
	}
	if typeErr != nil || len(request.Header.Values("Content-Type")) != 1 || contentType != "application/x-tar" || len(request.Header.Values("Content-Encoding")) != 0 {
		writeError(writer, http.StatusUnsupportedMediaType, "NATIVE_WORKSPACE_ARCHIVE_REQUIRED", "An unencoded portable tar archive is required.")
		return
	}
	if request.ContentLength > nativeWorkspaceArchiveBytes {
		writeError(writer, http.StatusRequestEntityTooLarge, "NATIVE_WORKSPACE_ARCHIVE_TOO_LARGE", "Snapshot archive exceeds the transport limit.")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, nativeWorkspaceArchiveBytes))
	if err != nil {
		writeError(writer, http.StatusRequestEntityTooLarge, "NATIVE_WORKSPACE_ARCHIVE_TOO_LARGE", "Snapshot archive could not be read within its bound.")
		return
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != hash {
		writeError(writer, http.StatusBadRequest, "NATIVE_WORKSPACE_SNAPSHOT_INVALID", "Snapshot transport hash does not match the received archive.")
		return
	}
	parsed, parseErr := scriptsandbox.ParseWorkspaceSnapshot(data, scriptsandbox.DefaultLimits())
	if parseErr != nil || parsed.SHA256 != hash {
		writeError(writer, http.StatusBadRequest, "NATIVE_WORKSPACE_SNAPSHOT_INVALID", "Snapshot must be canonical and match its exact transport hash.")
		return
	}
	saved, err := s.runtime.SaveNativeWorkspaceSnapshot(request.Context(), access, request.PathValue("request_id"), int(parent), data)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": nativeSnapshotReceipt(saved)})
}

func nativeSnapshotReceipt(snapshot businessruntime.NativeWorkspaceSnapshot) map[string]any {
	return map[string]any{"session_id": snapshot.SessionID, "version": snapshot.Version, "sha256": snapshot.Snapshot.SHA256}
}

func (s *Server) executeNativeWorkspaceCommand(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	var body *businessruntime.NativeWorkspaceCommandRequest
	if !decodeBodyWithLimit(writer, request, &body, 2<<20) {
		return
	}
	if body == nil {
		writeError(writer, http.StatusBadRequest, "NATIVE_WORKSPACE_REQUEST_INVALID", "Command request must be an object.")
		return
	}
	manager, ok := s.nativeWorkspaceManager(writer, request)
	if !ok {
		return
	}
	result, err := manager.ExecuteCommand(request.Context(), access, *body)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) executeNativeWorkspacePTY(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	var body *businessruntime.NativeWorkspacePTYRequest
	if !decodeBodyWithLimit(writer, request, &body, 256<<10) {
		return
	}
	if body == nil {
		writeError(writer, http.StatusBadRequest, "NATIVE_WORKSPACE_PTY_REQUEST_INVALID", "Terminal request must be an object.")
		return
	}
	manager, ok := s.nativeWorkspaceManager(writer, request)
	if !ok {
		return
	}
	result, err := manager.ExecutePTY(request.Context(), access, *body)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) terminateNativeWorkspacePTYs(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	var body *struct{}
	if !decodeBodyWithLimit(writer, request, &body, 4096) {
		return
	}
	if body == nil {
		writeError(writer, http.StatusBadRequest, "NATIVE_WORKSPACE_PTY_REQUEST_INVALID", "Terminal cleanup requires an empty object.")
		return
	}
	manager, ok := s.nativeWorkspaceManager(writer, request)
	if !ok {
		return
	}
	result, err := manager.TerminatePTYs(request.Context(), access)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) readNativeWorkspacePTYState(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	manager, ok := s.nativeWorkspaceManager(writer, request)
	if !ok {
		return
	}
	result, err := manager.ReadPTYState(request.Context(), access)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) nativeWorkspaceFile(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	var body *businessruntime.NativeWorkspaceFileRequest
	if !decodeBodyWithLimit(writer, request, &body, scriptsandbox.WorkspaceFileWireBytes) {
		return
	}
	if body == nil {
		writeError(writer, http.StatusBadRequest, "NATIVE_WORKSPACE_REQUEST_INVALID", "File request must be an object.")
		return
	}
	manager, ok := s.nativeWorkspaceManager(writer, request)
	if !ok {
		return
	}
	result, err := manager.File(request.Context(), access, *body)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func nativeSnapshotQuery(writer http.ResponseWriter, request *http.Request, key string, minimum int) (int, string, bool) {
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(query) != 2 || len(query[key]) != 1 || len(query["sha256"]) != 1 || !nativeWorkspaceHash.MatchString(query.Get("sha256")) {
		writeError(writer, http.StatusBadRequest, "NATIVE_WORKSPACE_REQUEST_INVALID", "Exact snapshot version and hash are required.")
		return 0, "", false
	}
	value, err := strconv.Atoi(query.Get(key))
	if err != nil || value < minimum || value > 128 || strconv.Itoa(value) != query.Get(key) {
		writeError(writer, http.StatusBadRequest, "NATIVE_WORKSPACE_REQUEST_INVALID", "Snapshot version is invalid.")
		return 0, "", false
	}
	return value, query.Get("sha256"), true
}

func (s *Server) readNativeSnapshot(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	version, err := strconv.Atoi(request.PathValue("version"))
	query, queryErr := url.ParseQuery(request.URL.RawQuery)
	if err != nil || queryErr != nil || version < 1 || version > 128 || strconv.Itoa(version) != request.PathValue("version") || len(query) != 1 || len(query["sha256"]) != 1 || !nativeWorkspaceHash.MatchString(query.Get("sha256")) {
		writeError(writer, http.StatusBadRequest, "NATIVE_WORKSPACE_REQUEST_INVALID", "Exact snapshot version and hash are required.")
		return
	}
	result, err := s.runtime.ReadNativeWorkspaceSnapshot(request.Context(), access, version, query.Get("sha256"))
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	writeNativeArchive(writer, access.SessionID, version, result.Snapshot)
}

func (s *Server) readNativeSnapshotReceipt(writer http.ResponseWriter, request *http.Request) {
	access, ok := s.nativeWorkspaceAccess(writer, request)
	if !ok {
		return
	}
	parent, hash, ok := nativeSnapshotQuery(writer, request, "parent_version", 0)
	if !ok {
		return
	}
	result, found, err := s.runtime.ReadNativeWorkspaceSnapshotReceipt(request.Context(), access, request.PathValue("request_id"), parent, hash)
	if err != nil {
		writeNativeWorkspaceError(writer, err)
		return
	}
	var receipt any
	if found {
		receipt = nativeSnapshotReceipt(result)
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"receipt": receipt}})
}

func writeNativeArchive(writer http.ResponseWriter, sessionID string, version int, snapshot scriptsandbox.WorkspaceSnapshot) {
	writer.Header().Set("Content-Type", "application/x-tar")
	writer.Header().Set("Content-Length", strconv.Itoa(len(snapshot.Archive)))
	writer.Header().Set("X-Workspace-Session-ID", sessionID)
	writer.Header().Set("X-Workspace-Snapshot-Version", strconv.Itoa(version))
	writer.Header().Set("X-Workspace-Snapshot-Sha256", snapshot.SHA256)
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(snapshot.Archive)
}

func writeNativeWorkspaceError(writer http.ResponseWriter, err error) {
	var domain *businessruntime.DomainError
	if errors.As(err, &domain) {
		switch {
		case domain.Code == "NATIVE_WORKSPACE_MANIFEST_INVALID", domain.Code == "NATIVE_WORKSPACE_PTY_REQUEST_INVALID":
			writeError(writer, http.StatusBadRequest, domain.Code, domain.Message)
			return
		case domain.Code == "NATIVE_WORKSPACE_MANIFEST_CONFLICT", domain.Code == "NATIVE_WORKSPACE_MANIFEST_UNCONFIRMED", domain.Code == "NATIVE_WORKSPACE_RESOURCE_CHANGED":
			writeError(writer, http.StatusConflict, domain.Code, domain.Message)
			return
		case domain.Code == "NATIVE_WORKSPACE_COMMAND_APPROVAL_REQUIRED", domain.Code == "NATIVE_WORKSPACE_FILE_APPROVAL_REQUIRED":
			writeError(writer, http.StatusForbidden, domain.Code, domain.Message)
			return
		case domain.Code == "NATIVE_WORKSPACE_COMMAND_UNCONFIRMED", domain.Code == "NATIVE_WORKSPACE_FILE_UNCONFIRMED", domain.Code == "NATIVE_WORKSPACE_PTY_UNCONFIRMED", domain.Code == "NATIVE_WORKSPACE_PTY_SESSION_LOST", domain.Code == "NATIVE_WORKSPACE_PTY_STDIN_UNAVAILABLE":
			writeError(writer, http.StatusConflict, domain.Code, domain.Message)
			return
		case strings.HasPrefix(domain.Code, "NATIVE_WORKSPACE_LEASE_"), domain.Code == "NATIVE_WORKSPACE_EXECUTION_STALE", domain.Code == "NATIVE_WORKSPACE_SCOPE_MISMATCH":
			writeError(writer, http.StatusForbidden, domain.Code, domain.Message)
			return
		case domain.Code == "NATIVE_WORKSPACE_POLICY_DISABLED", domain.Code == "NATIVE_WORKSPACE_UNAVAILABLE":
			writeError(writer, http.StatusServiceUnavailable, domain.Code, domain.Message)
			return
		case domain.Code == "NATIVE_WORKSPACE_RECOVERY_REQUIRED", domain.Code == "NATIVE_WORKSPACE_BUSY", domain.Code == "NATIVE_WORKSPACE_OPERATION_IN_PROGRESS", domain.Code == "NATIVE_WORKSPACE_POLICY_CHANGED", domain.Code == "NATIVE_WORKSPACE_EXECUTION_PAUSING":
			writeError(writer, http.StatusConflict, domain.Code, domain.Message)
			return
		case domain.Code == "NATIVE_WORKSPACE_QUOTA_EXCEEDED":
			writeError(writer, http.StatusTooManyRequests, domain.Code, domain.Message)
			return
		case domain.Code == "NATIVE_WORKSPACE_ENVIRONMENT_NOT_FOUND", domain.Code == "NATIVE_WORKSPACE_SNAPSHOT_NOT_FOUND":
			writeError(writer, http.StatusNotFound, domain.Code, domain.Message)
			return
		case domain.Code == "NATIVE_WORKSPACE_CONFLICT", domain.Code == "NATIVE_WORKSPACE_REQUEST_CONFLICT", domain.Code == "NATIVE_WORKSPACE_SNAPSHOT_CONFLICT", domain.Code == "NATIVE_WORKSPACE_OPERATION_SUPERSEDED":
			writeError(writer, http.StatusConflict, domain.Code, domain.Message)
			return
		}
		writeRuntimeError(writer, err)
		return
	}
	var sandboxError *scriptsandbox.Error
	if errors.As(err, &sandboxError) {
		if sandboxError.Code == "WORKSPACE_PTY_REQUEST_INVALID" {
			writeError(writer, http.StatusBadRequest, sandboxError.Code, "The terminal request is outside the bounded workspace contract.")
			return
		}
		if sandboxError.Code == "WORKSPACE_FILE_REQUEST_INVALID" {
			writeError(writer, http.StatusBadRequest, sandboxError.Code, "The file request is outside the bounded workspace contract.")
			return
		}
		writeError(writer, http.StatusConflict, sandboxError.Code, "The isolated workspace operation did not return a confirmed result.")
		return
	}
	writeError(writer, http.StatusInternalServerError, "NATIVE_WORKSPACE_OPERATION_UNCONFIRMED", "The workspace operation could not be confirmed.")
}
