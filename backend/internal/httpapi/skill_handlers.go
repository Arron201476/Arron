package httpapi

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	agentruntime "content-agent/backend/internal/runtime"
)

func (s *Server) skillManagementOptions(writer http.ResponseWriter, request *http.Request) {
	scopes := make([]string, 0)
	if principal, ok := identity.UserFromContext(request.Context()); ok {
		if principal.Allows(identity.RoleEditor) {
			scopes = append(scopes, "user", "project")
		}
		if principal.Allows(identity.RoleAdmin) {
			scopes = append(scopes, "workspace")
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"install_scopes": scopes, "system_read_only": true, "directory_updates": true, "lifecycle_commands": true, "package_commands": true}})
}

func (s *Server) previewSkillDirectoryUpdate(writer http.ResponseWriter, request *http.Request) {
	preview, err := s.runtime.PreviewSkillDirectoryUpdate(request.Context(), request.PathValue("skill_installation_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": preview})
}

func (s *Server) updateSkillFromDirectory(writer http.ResponseWriter, request *http.Request) {
	installationID := request.PathValue("skill_installation_id")
	if _, err := s.runtime.PreviewSkillLifecycle(request.Context(), installationID); err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	var command struct {
		agentruntime.UpdateSkillFromDirectoryCommand
		Expected *agentruntime.SkillLifecycleSnapshot `json:"expected"`
	}
	if !decodeBody(writer, request, &command) {
		return
	}
	if command.Expected == nil || command.Expected.ActiveVersionID != command.ExpectedActiveVersionID {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "目录更新缺少一致的安装状态。")
		return
	}
	meta, ok := commandMeta(writer, request, installationID, "skill_package", command)
	if !ok {
		return
	}
	item, err := s.runtime.ExecuteSkillPackageCommand(request.Context(), agentruntime.SkillPackageCommand{Action: "update_directory", InstallationID: installationID, Expected: command.Expected, Version: command.Version, ContentHash: command.ContentHash, IdempotencyKey: meta.IdempotencyKey}, nil)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": item})
}

func (s *Server) listSkills(writer http.ResponseWriter, request *http.Request) {
	includeUninstalled := false
	if raw := strings.TrimSpace(request.URL.Query().Get("include_uninstalled")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "include_uninstalled 必须是布尔值。")
			return
		}
		includeUninstalled = value
	}
	items, err := s.runtime.ListSkillInstallations(request.Context(), includeUninstalled)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) refreshSkills(writer http.ResponseWriter, request *http.Request) {
	diagnostics, err := s.runtime.RefreshWorkspaceSkills(request.Context(), request.URL.Query().Get("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"diagnostics": diagnostics}})
}

func (s *Server) installDiscoveredSkill(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Version     string `json:"version"`
		ContentHash string `json:"content_hash"`
		agentruntime.SkillInstallTarget
	}
	if !decodeBody(writer, request, &input) {
		return
	}
	meta, ok := commandMeta(writer, request, request.PathValue("capability_id"), "skill_package", input)
	if !ok {
		return
	}
	item, err := s.runtime.ExecuteSkillPackageCommand(request.Context(), agentruntime.SkillPackageCommand{Action: "adopt_directory", CapabilityID: request.PathValue("capability_id"), Version: input.Version, ContentHash: input.ContentHash, Target: input.SkillInstallTarget, IdempotencyKey: meta.IdempotencyKey}, nil)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": item})
}

func (s *Server) getSkill(writer http.ResponseWriter, request *http.Request) {
	item, err := s.runtime.GetSkillInstallation(
		request.Context(), request.PathValue("skill_installation_id"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": item})
}

func (s *Server) installSkill(writer http.ResponseWriter, request *http.Request) {
	if !acceptSkillZIP(writer, request) {
		return
	}
	command := agentruntime.SkillPackageCommand{Action: "install_zip", SourceName: skillUploadName(request), Target: agentruntime.SkillInstallTarget{Scope: capability.SkillScope(request.URL.Query().Get("scope")), ProjectID: request.URL.Query().Get("project_id")}}
	meta, ok := commandMeta(writer, request, "skills", "skill_package", nil)
	if !ok {
		return
	}
	command.IdempotencyKey = meta.IdempotencyKey
	item, err := s.runtime.ExecuteSkillPackageCommand(request.Context(), command, request.Body)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": item})
}

func (s *Server) upgradeSkill(writer http.ResponseWriter, request *http.Request) {
	if !acceptSkillZIP(writer, request) {
		return
	}
	installationID := request.PathValue("skill_installation_id")
	if _, err := s.runtime.PreviewSkillLifecycle(request.Context(), installationID); err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	var expected *agentruntime.SkillLifecycleSnapshot
	rawExpected := request.Header.Get("X-Skill-Expected")
	if len(rawExpected) > 4096 {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "Skill 安装状态过大。")
		return
	}
	decoder := json.NewDecoder(strings.NewReader(rawExpected))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&expected); err != nil || expected == nil {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "Skill 升级缺少安装状态。")
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "Skill 安装状态包含多余数据。")
		return
	}
	meta, ok := commandMeta(writer, request, installationID, "skill_package", expected)
	if !ok {
		return
	}
	item, err := s.runtime.ExecuteSkillPackageCommand(request.Context(), agentruntime.SkillPackageCommand{Action: "upgrade_zip", InstallationID: installationID, Expected: expected, SourceName: skillUploadName(request), IdempotencyKey: meta.IdempotencyKey}, request.Body)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": item})
}

func (s *Server) enableSkill(writer http.ResponseWriter, request *http.Request) {
	s.setSkillEnabled(writer, request, true)
}

func (s *Server) disableSkill(writer http.ResponseWriter, request *http.Request) {
	s.setSkillEnabled(writer, request, false)
}

func (s *Server) setSkillEnabled(writer http.ResponseWriter, request *http.Request, enabled bool) {
	action := "disable"
	if enabled {
		action = "enable"
	}
	s.controlSkillInstallation(writer, request, action, "")
}

func (s *Server) activateSkillVersion(writer http.ResponseWriter, request *http.Request) {
	s.controlSkillInstallation(writer, request, "activate", request.PathValue("version"))
}

func (s *Server) uninstallSkill(writer http.ResponseWriter, request *http.Request) {
	s.controlSkillInstallation(writer, request, "uninstall", "")
}

func (s *Server) controlSkillInstallation(writer http.ResponseWriter, request *http.Request, action, version string) {
	installationID := request.PathValue("skill_installation_id")
	// Check current scope authority even for malformed or replayed requests.
	if _, err := s.runtime.PreviewSkillLifecycle(request.Context(), installationID); err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	var input struct {
		Expected *agentruntime.SkillLifecycleSnapshot `json:"expected"`
	}
	if !decodeBody(writer, request, &input) {
		return
	}
	meta, ok := commandMeta(writer, request, installationID, "skill_lifecycle", input)
	if !ok {
		return
	}
	item, err := s.runtime.ControlSkillInstallation(request.Context(), agentruntime.SkillLifecycleCommand{InstallationID: installationID, Action: action, Version: version, Expected: input.Expected, IdempotencyKey: meta.IdempotencyKey})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": item})
}

func (s *Server) listSkillInstallAttempts(writer http.ResponseWriter, request *http.Request) {
	limit := 50
	if raw := strings.TrimSpace(request.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "limit 必须是正整数。")
			return
		}
		limit = value
	}
	items, err := s.runtime.ListSkillInstallAttempts(request.Context(), limit)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func acceptSkillZIP(writer http.ResponseWriter, request *http.Request) bool {
	contentType := strings.TrimSpace(request.Header.Get("Content-Type"))
	if contentType == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "application/zip" &&
		mediaType != "application/x-zip-compressed" &&
		mediaType != "application/octet-stream") {
		writeError(writer, http.StatusUnsupportedMediaType, "SKILL_ARCHIVE_REQUIRED", "Skill 安装请求必须上传 ZIP。")
		return false
	}
	return true
}

func skillUploadName(request *http.Request) string {
	if name := strings.TrimSpace(request.Header.Get("X-Skill-Filename")); name != "" {
		return name
	}
	if disposition := request.Header.Get("Content-Disposition"); disposition != "" {
		if _, parameters, err := mime.ParseMediaType(disposition); err == nil {
			if name := strings.TrimSpace(parameters["filename"]); name != "" {
				return name
			}
		}
	}
	return "skill.zip"
}
