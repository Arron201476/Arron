package agenttool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"content-agent/backend/internal/capability"
)

const (
	SchemaVersion  = "1.0.0"
	maxConfigBytes = 1024 * 1024
)

type Kind string

const (
	KindRuntimeFunction Kind = "runtime_function"
	KindMCP             Kind = "mcp"
	KindHosted          Kind = "hosted"
)

type Access string

const (
	AccessRead      Access = "read"
	AccessWrite     Access = "write"
	AccessSensitive Access = "sensitive"
)

type ApprovalPolicy string

const (
	ApprovalNever  ApprovalPolicy = "never"
	ApprovalAlways ApprovalPolicy = "always"
)

type Config struct {
	Programmatic  ProgrammaticConfig `json:"programmatic_tool_calling"`
	SchemaVersion string             `json:"schema_version"`
	MCPServers    []MCPServerConfig  `json:"mcp_servers"`
	HostedTools   []HostedToolConfig `json:"hosted_tools"`
	StdioPolicy   StdioPolicy        `json:"stdio_policy"`
}

type MCPServerConfig struct {
	WorkspaceIDs          []string          `json:"workspace_ids,omitempty"`
	ID                    string            `json:"id"`
	Description           string            `json:"description"`
	Transport             string            `json:"transport"`
	URL                   string            `json:"url,omitempty"`
	Command               string            `json:"command,omitempty"`
	Args                  []string          `json:"args,omitempty"`
	Cwd                   string            `json:"cwd,omitempty"`
	Environment           map[string]string `json:"environment,omitempty"`
	HeaderEnvironment     map[string]string `json:"header_environment,omitempty"`
	CredentialEnvironment map[string]string `json:"credential_environment,omitempty"`
	CredentialHeaders     map[string]string `json:"credential_headers,omitempty"`
	CredentialBinding     string            `json:"credential_binding,omitempty"`
	Enabled               bool              `json:"enabled"`
	AllowedTools          []MCPToolConfig   `json:"allowed_tools"`
	DeferLoading          bool              `json:"defer_loading"`
	TimeoutSeconds        int               `json:"timeout_seconds"`
	MaxRetries            int               `json:"max_retries"`
	MaxResultBytes        int               `json:"max_result_bytes"`
}

type MCPToolConfig struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Access      Access         `json:"access"`
	Approval    ApprovalPolicy `json:"approval,omitempty"`
}

type HostedToolConfig struct {
	WorkspaceIDs   []string       `json:"workspace_ids,omitempty"`
	ID             string         `json:"id"`
	Type           string         `json:"type"`
	Description    string         `json:"description"`
	Enabled        bool           `json:"enabled"`
	Access         Access         `json:"access"`
	Approval       ApprovalPolicy `json:"approval,omitempty"`
	DeferLoading   bool           `json:"defer_loading"`
	TimeoutSeconds int            `json:"timeout_seconds"`
	MaxResultBytes int            `json:"max_result_bytes"`
	VectorStoreIDs []string       `json:"vector_store_ids,omitempty"`
	MaxNumResults  int            `json:"max_num_results,omitempty"`
}

type StdioPolicy struct {
	Enabled         bool     `json:"enabled"`
	AllowedCommands []string `json:"allowed_commands"`
	AllowedCwds     []string `json:"allowed_cwds"`
}

type Descriptor struct {
	ConfigurationHash string         `json:"configuration_hash,omitempty"`
	ID                string         `json:"id"`
	Kind              Kind           `json:"kind"`
	ServerID          string         `json:"server_id,omitempty"`
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	Transport         string         `json:"transport,omitempty"`
	Access            Access         `json:"access"`
	Approval          ApprovalPolicy `json:"approval"`
	Enabled           bool           `json:"enabled"`
	DeferLoading      bool           `json:"defer_loading"`
	TimeoutSeconds    int            `json:"timeout_seconds"`
	MaxRetries        int            `json:"max_retries"`
	MaxResultBytes    int            `json:"max_result_bytes"`
}

type Catalog struct {
	ProgrammaticToolIDs []string           `json:"programmatic_tool_ids,omitempty"`
	SchemaVersion       string             `json:"schema_version"`
	Tools               []Descriptor       `json:"tools"`
	MCPServers          []MCPServerConfig  `json:"mcp_servers,omitempty"`
	HostedTools         []HostedToolConfig `json:"hosted_tools,omitempty"`
}

type Registry struct {
	config      Config
	descriptors []Descriptor
	mcpServers  map[string]MCPServerConfig
	mcpTools    map[string]Descriptor
}

var identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

func Load(path string) (*Registry, error) {
	config := Config{SchemaVersion: SchemaVersion}
	path = strings.TrimSpace(path)
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read Agent tool config: %w", err)
		}
		if len(data) > maxConfigBytes {
			return nil, errors.New("Agent tool config exceeds 1 MiB")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&config); err != nil {
			return nil, fmt.Errorf("decode Agent tool config: %w", err)
		}
		if err := ensureEOF(decoder); err != nil {
			return nil, fmt.Errorf("decode Agent tool config: %w", err)
		}
	}
	return Compile(config)
}

func Compile(config Config) (*Registry, error) {
	if config.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("Agent tool config schema_version must be %s", SchemaVersion)
	}
	config.MCPServers = slices.Clone(config.MCPServers)
	for index := range config.MCPServers {
		config.MCPServers[index] = cloneMCPServer(config.MCPServers[index])
	}
	config.HostedTools = cloneHostedTools(config.HostedTools)
	config.Programmatic = cloneProgrammaticConfig(config.Programmatic)
	config.StdioPolicy.AllowedCommands = slices.Clone(config.StdioPolicy.AllowedCommands)
	config.StdioPolicy.AllowedCwds = slices.Clone(config.StdioPolicy.AllowedCwds)
	registry := &Registry{
		config:     config,
		mcpServers: make(map[string]MCPServerConfig),
		mcpTools:   make(map[string]Descriptor),
	}
	registry.descriptors = runtimeFunctionDescriptors()
	seenIDs := make(map[string]struct{}, len(registry.descriptors))
	for _, descriptor := range registry.descriptors {
		seenIDs[descriptor.ID] = struct{}{}
	}

	if err := normalizeStdioPolicy(&registry.config.StdioPolicy); err != nil {
		return nil, err
	}
	for index := range registry.config.MCPServers {
		server := &registry.config.MCPServers[index]
		if err := normalizeMCPServer(server, registry.config.StdioPolicy); err != nil {
			return nil, fmt.Errorf("mcp_servers[%d]: %w", index, err)
		}
		if _, duplicate := registry.mcpServers[server.ID]; duplicate {
			return nil, fmt.Errorf("mcp server id %q is duplicated", server.ID)
		}
		registry.mcpServers[server.ID] = cloneMCPServer(*server)
		for _, tool := range server.AllowedTools {
			id := "mcp:" + server.ID + "/" + tool.Name
			if _, duplicate := seenIDs[id]; duplicate {
				return nil, fmt.Errorf("tool id %q is duplicated", id)
			}
			seenIDs[id] = struct{}{}
			descriptor := Descriptor{
				ID: id, Kind: KindMCP, ServerID: server.ID, Name: tool.Name,
				Description: tool.Description, Transport: server.Transport,
				Access: tool.Access, Approval: tool.Approval, Enabled: server.Enabled,
				DeferLoading: server.DeferLoading, TimeoutSeconds: server.TimeoutSeconds,
				MaxRetries: server.MaxRetries, MaxResultBytes: server.MaxResultBytes,
			}
			registry.descriptors = append(registry.descriptors, descriptor)
			registry.mcpTools[server.ID+"/"+tool.Name] = descriptor
		}
	}
	for index := range registry.config.HostedTools {
		tool := &registry.config.HostedTools[index]
		if err := normalizeHostedTool(tool); err != nil {
			return nil, fmt.Errorf("hosted_tools[%d]: %w", index, err)
		}
		id := "hosted:" + tool.ID
		if _, duplicate := seenIDs[id]; duplicate {
			return nil, fmt.Errorf("tool id %q is duplicated", id)
		}
		seenIDs[id] = struct{}{}
		registry.descriptors = append(registry.descriptors, Descriptor{
			ID: id, Kind: KindHosted, Name: tool.Type, Description: tool.Description,
			Access: tool.Access, Approval: tool.Approval, Enabled: tool.Enabled,
			DeferLoading: tool.DeferLoading, TimeoutSeconds: tool.TimeoutSeconds,
			MaxResultBytes: tool.MaxResultBytes,
		})
	}
	sort.SliceStable(registry.descriptors, func(left, right int) bool {
		return registry.descriptors[left].ID < registry.descriptors[right].ID
	})
	if err := registry.validateProgrammaticConfig(); err != nil {
		return nil, err
	}
	registry.bindConfigurationHashes()
	return registry, nil
}

func (r *Registry) PublicCatalog() Catalog {
	return Catalog{SchemaVersion: SchemaVersion, Tools: slices.Clone(r.descriptors)}
}

func (r *Registry) Get(id string) (Descriptor, bool) {
	for _, descriptor := range r.descriptors {
		if descriptor.ID == id {
			return descriptor, true
		}
	}
	return Descriptor{}, false
}

func (r *Registry) PrivateCatalog() Catalog {
	servers := make([]MCPServerConfig, 0, len(r.config.MCPServers))
	for _, server := range r.config.MCPServers {
		servers = append(servers, cloneMCPServer(server))
	}
	return Catalog{
		ProgrammaticToolIDs: r.programmaticToolIDs(),
		SchemaVersion:       SchemaVersion,
		Tools:               slices.Clone(r.descriptors),
		MCPServers:          servers,
		HostedTools:         cloneHostedTools(r.config.HostedTools),
	}
}

func (r *Registry) ResolveSkillDependencies(dependencies []capability.SkillDependency) capability.SkillDependencyResolution {
	resolved := slices.Clone(dependencies)
	available := true
	reasonCode := ""
	message := ""
	for index := range resolved {
		dependency := &resolved[index]
		dependency.Status = capability.Unavailable
		if dependency.Type != "mcp" {
			dependency.ReasonCode = "SKILL_TOOL_DEPENDENCY_UNSUPPORTED"
			dependency.UserMessage = "该 Skill 使用了平台不支持的工具依赖类型。"
		} else if dependency.URL != "" {
			dependency.ReasonCode = "SKILL_TOOL_URL_FORBIDDEN"
			dependency.UserMessage = "Skill 不能直接声明工具地址，请由管理员配置可信 MCP 服务。"
		} else if !validDependencyReference(dependency.Value) {
			dependency.ReasonCode = "SKILL_TOOL_REFERENCE_INVALID"
			dependency.UserMessage = "MCP 工具依赖必须使用 server/tool 格式。"
		} else if descriptor, ok := r.mcpTools[dependency.Value]; !ok {
			dependency.ReasonCode = "SKILL_TOOL_DEPENDENCY_MISSING"
			dependency.UserMessage = fmt.Sprintf("缺少 MCP 工具 %s，请由管理员配置并启用。", dependency.Value)
		} else if !descriptor.Enabled {
			dependency.ReasonCode = "SKILL_TOOL_DEPENDENCY_DISABLED"
			dependency.UserMessage = fmt.Sprintf("MCP 工具 %s 当前已禁用。", dependency.Value)
		} else if dependency.Transport != "" && dependency.Transport != descriptor.Transport {
			dependency.ReasonCode = "SKILL_TOOL_TRANSPORT_MISMATCH"
			dependency.UserMessage = fmt.Sprintf("MCP 工具 %s 的传输方式与平台配置不一致。", dependency.Value)
		} else {
			dependency.Status = capability.Available
			dependency.ReasonCode = ""
			dependency.UserMessage = ""
			continue
		}
		if available {
			reasonCode = dependency.ReasonCode
			message = dependency.UserMessage
		}
		available = false
	}
	return capability.SkillDependencyResolution{
		Dependencies: resolved,
		Available:    available,
		ReasonCode:   reasonCode,
		Message:      message,
	}
}

func normalizeMCPServer(server *MCPServerConfig, stdio StdioPolicy) error {
	server.ID = strings.TrimSpace(server.ID)
	server.Description = strings.TrimSpace(server.Description)
	server.Transport = strings.TrimSpace(server.Transport)
	server.URL = strings.TrimSpace(server.URL)
	server.Command = strings.TrimSpace(server.Command)
	server.Cwd = strings.TrimSpace(server.Cwd)
	if !identifierPattern.MatchString(server.ID) {
		return errors.New("id must be a lowercase tool identifier")
	}
	if server.Description == "" {
		return errors.New("description is required")
	}
	if server.TimeoutSeconds == 0 {
		server.TimeoutSeconds = 10
	}
	if server.MaxResultBytes == 0 {
		server.MaxResultBytes = 64 * 1024
	}
	if server.TimeoutSeconds < 1 || server.TimeoutSeconds > 300 {
		return errors.New("timeout_seconds must be between 1 and 300")
	}
	if server.MaxRetries < 0 || server.MaxRetries > 5 {
		return errors.New("max_retries must be between 0 and 5")
	}
	if server.MaxResultBytes < 1024 || server.MaxResultBytes > 1024*1024 {
		return errors.New("max_result_bytes must be between 1024 and 1048576")
	}
	switch server.Transport {
	case "streamable_http", "sse":
		if err := validateHTTPURL(server.URL); err != nil {
			return err
		}
		if server.Command != "" || server.Cwd != "" || len(server.Args) > 0 || len(server.Environment) > 0 || len(server.CredentialEnvironment) > 0 {
			return errors.New("HTTP MCP server cannot declare stdio process fields")
		}
	case "stdio":
		if server.URL != "" || len(server.HeaderEnvironment) > 0 || len(server.CredentialHeaders) > 0 {
			return errors.New("stdio MCP server cannot declare HTTP fields")
		}
		if err := validateStdio(server, stdio); err != nil {
			return err
		}
	default:
		return errors.New("transport must be streamable_http, sse, or stdio")
	}
	seen := make(map[string]struct{}, len(server.AllowedTools))
	if len(server.AllowedTools) == 0 {
		return errors.New("allowed_tools cannot be empty")
	}
	for index := range server.AllowedTools {
		tool := &server.AllowedTools[index]
		tool.Name = strings.TrimSpace(tool.Name)
		tool.Description = strings.TrimSpace(tool.Description)
		if !identifierPattern.MatchString(tool.Name) {
			return fmt.Errorf("allowed_tools[%d].name is invalid", index)
		}
		if _, duplicate := seen[tool.Name]; duplicate {
			return fmt.Errorf("allowed tool %q is duplicated", tool.Name)
		}
		seen[tool.Name] = struct{}{}
		if tool.Description == "" {
			return fmt.Errorf("allowed_tools[%d].description is required", index)
		}
		if err := normalizePolicy(&tool.Access, &tool.Approval); err != nil {
			return fmt.Errorf("allowed_tools[%d]: %w", index, err)
		}
	}
	if server.MaxRetries > 0 {
		for _, tool := range server.AllowedTools {
			if tool.Approval == ApprovalAlways {
				return errors.New("max_retries must be 0 when an allowed tool requires approval")
			}
		}
	}
	for key, envName := range server.Environment {
		if strings.TrimSpace(key) == "" || !validEnvironmentName(envName) {
			return errors.New("environment values must name explicit environment variables")
		}
	}
	for header, envName := range server.HeaderEnvironment {
		if strings.TrimSpace(header) == "" || !validEnvironmentName(envName) {
			return errors.New("header_environment values must name explicit environment variables")
		}
	}
	if err := validateMCPCredentialTargets(*server); err != nil {
		return err
	}
	return nil
}

func normalizeHostedTool(tool *HostedToolConfig) error {
	tool.ID = strings.TrimSpace(tool.ID)
	tool.Type = strings.TrimSpace(tool.Type)
	tool.Description = strings.TrimSpace(tool.Description)
	if !identifierPattern.MatchString(tool.ID) || !identifierPattern.MatchString(tool.Type) {
		return errors.New("id and type must be lowercase tool identifiers")
	}
	allowedTypes := map[string]struct{}{
		"web_search": {}, "file_search": {}, "code_interpreter": {}, "image_generation": {},
	}
	if _, ok := allowedTypes[tool.Type]; !ok {
		return fmt.Errorf("unsupported hosted tool type %q", tool.Type)
	}
	if tool.Description == "" {
		return errors.New("description is required")
	}
	if tool.TimeoutSeconds == 0 {
		tool.TimeoutSeconds = 60
	}
	if tool.MaxResultBytes == 0 {
		tool.MaxResultBytes = 256 * 1024
	}
	if tool.TimeoutSeconds < 1 || tool.TimeoutSeconds > 600 {
		return errors.New("timeout_seconds must be between 1 and 600")
	}
	if tool.MaxResultBytes < 1024 || tool.MaxResultBytes > 1024*1024 {
		return errors.New("max_result_bytes must be between 1024 and 1048576")
	}
	if err := normalizePolicy(&tool.Access, &tool.Approval); err != nil {
		return err
	}
	if tool.Type == "file_search" {
		if tool.Enabled && len(tool.VectorStoreIDs) == 0 {
			return errors.New("enabled file_search requires vector_store_ids")
		}
		if len(tool.VectorStoreIDs) > 2 {
			return errors.New("file_search supports at most two vector stores")
		}
		seen := make(map[string]bool)
		for _, id := range tool.VectorStoreIDs {
			if !regexp.MustCompile(`^vs_[a-zA-Z0-9_-]{1,128}$`).MatchString(id) || seen[id] {
				return errors.New("vector_store_ids must contain unique vector store identifiers")
			}
			seen[id] = true
		}
		if tool.MaxNumResults == 0 {
			tool.MaxNumResults = 10
		}
		if tool.MaxNumResults < 1 || tool.MaxNumResults > 50 {
			return errors.New("max_num_results must be between 1 and 50")
		}
	} else if len(tool.VectorStoreIDs) != 0 || tool.MaxNumResults != 0 {
		return errors.New("vector_store_ids and max_num_results are only valid for file_search")
	}
	if tool.Type == "code_interpreter" || tool.Type == "image_generation" {
		if tool.Access == AccessRead || tool.Approval != ApprovalAlways {
			return errors.New("code and image tools require write/sensitive access and approval=always")
		}
	} else if tool.Access != AccessRead && tool.Approval != ApprovalAlways {
		return errors.New("non-read hosted tools require approval=always")
	}
	return nil
}

func cloneHostedTools(source []HostedToolConfig) []HostedToolConfig {
	result := slices.Clone(source)
	for index := range result {
		result[index].WorkspaceIDs = slices.Clone(result[index].WorkspaceIDs)
		result[index].VectorStoreIDs = slices.Clone(result[index].VectorStoreIDs)
	}
	return result
}

func normalizePolicy(access *Access, approval *ApprovalPolicy) error {
	if *access == "" {
		*access = AccessSensitive
	}
	if *access != AccessRead && *access != AccessWrite && *access != AccessSensitive {
		return errors.New("access must be read, write, or sensitive")
	}
	if *approval == "" {
		if *access == AccessRead {
			*approval = ApprovalNever
		} else {
			*approval = ApprovalAlways
		}
	}
	if *approval != ApprovalNever && *approval != ApprovalAlways {
		return errors.New("approval must be never or always")
	}
	return nil
}

func validateHTTPURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("url must be an absolute HTTP URL")
	}
	if parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return errors.New("url cannot contain credentials, query parameters, or fragments")
	}
	host := parsed.Hostname()
	loopback := strings.EqualFold(host, "localhost")
	if ip := net.ParseIP(host); ip != nil {
		loopback = ip.IsLoopback()
	}
	if parsed.Scheme == "http" && !loopback {
		return errors.New("remote MCP url must use https")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return errors.New("url scheme must be https, or http for loopback only")
	}
	return nil
}

func normalizeStdioPolicy(policy *StdioPolicy) error {
	for index, command := range policy.AllowedCommands {
		absolute, err := filepath.Abs(strings.TrimSpace(command))
		if err != nil || !filepath.IsAbs(strings.TrimSpace(command)) {
			return fmt.Errorf("stdio_policy.allowed_commands[%d] must be absolute", index)
		}
		policy.AllowedCommands[index] = filepath.Clean(absolute)
	}
	for index, cwd := range policy.AllowedCwds {
		absolute, err := filepath.Abs(strings.TrimSpace(cwd))
		if err != nil || !filepath.IsAbs(strings.TrimSpace(cwd)) {
			return fmt.Errorf("stdio_policy.allowed_cwds[%d] must be absolute", index)
		}
		policy.AllowedCwds[index] = filepath.Clean(absolute)
	}
	return nil
}

func validateStdio(server *MCPServerConfig, policy StdioPolicy) error {
	if !policy.Enabled {
		return errors.New("stdio transport is disabled by policy")
	}
	if !filepath.IsAbs(server.Command) {
		return errors.New("stdio command must be absolute")
	}
	server.Command = filepath.Clean(server.Command)
	if !containsPath(policy.AllowedCommands, server.Command) {
		return errors.New("stdio command is not allowlisted")
	}
	if server.Cwd == "" || !filepath.IsAbs(server.Cwd) {
		return errors.New("stdio cwd must be absolute")
	}
	server.Cwd = filepath.Clean(server.Cwd)
	allowed := false
	for _, root := range policy.AllowedCwds {
		if pathWithin(root, server.Cwd) {
			allowed = true
			break
		}
	}
	if !allowed {
		return errors.New("stdio cwd is outside allowlisted roots")
	}
	return nil
}

func runtimeFunctionDescriptors() []Descriptor {
	reads := []string{
		"list_capabilities", "load_skill_instructions", "inspect_project",
		"list_skill_resources", "read_skill_resource", "view_image",
		"inspect_project_goal", "list_project_assets", "inspect_text_asset",
		"search_artifacts", "inspect_current_artifact", "inspect_artifact_version",
		"get_artifact_downloads",
		"inspect_run", "inspect_recent_conversation", "search_conversation_history",
		"list_workspace_files", "read_workspace_file", "prepare_workspace_publication", "prepare_agent_memory_publication",
		"validate_workspace_skill",
		"list_execution_targets", "inspect_execution_controls",
		"get_saved_instructions",
	}
	result := make([]Descriptor, 0, len(reads)+3)
	for _, name := range reads {
		maxResultBytes := 256 * 1024
		if name == "read_skill_resource" || name == "view_image" {
			// A bounded 10 MiB resource becomes up to 14 MiB of base64 in transit.
			maxResultBytes = 16 * 1024 * 1024
		}
		if name == "prepare_workspace_publication" {
			maxResultBytes = 512 * 1024
		}
		result = append(result, Descriptor{
			ID: "runtime:" + name, Kind: KindRuntimeFunction, Name: name,
			Description: "Go Runtime authoritative function tool", Access: AccessRead,
			Approval: ApprovalNever, Enabled: true, TimeoutSeconds: 30,
			MaxResultBytes: maxResultBytes,
		})
	}
	for _, name := range []string{"set_episode_execution_mode", "commit_agent_action", "apply_workspace_patch"} {
		result = append(result, Descriptor{
			ID: "runtime:" + name, Kind: KindRuntimeFunction, Name: name,
			Description: "Go Runtime authoritative command tool", Access: AccessWrite,
			Approval: ApprovalNever, Enabled: true, TimeoutSeconds: 60,
			MaxResultBytes: 256 * 1024,
		})
	}
	result = append(result, Descriptor{
		ID: "runtime:delegate_subtask", Kind: KindRuntimeFunction, Name: "delegate_subtask",
		Description: "Delegate bounded project analysis or drafting to an SDK subagent; parent retains all writes",
		Access:      AccessRead, Approval: ApprovalNever, Enabled: true,
		TimeoutSeconds: 180, MaxResultBytes: 256 * 1024,
	})
	result = append(result, Descriptor{
		ID: "runtime:control_execution", Kind: KindRuntimeFunction, Name: "control_execution",
		Description: "Control an inspected Run or background task after user approval", Access: AccessWrite,
		Approval: ApprovalAlways, Enabled: true, TimeoutSeconds: 60, MaxResultBytes: 256 * 1024,
	})
	result = append(result, Descriptor{
		ID: "runtime:update_saved_instructions", Kind: KindRuntimeFunction, Name: "update_saved_instructions",
		Description: "Save, revise or disable scoped Agent instructions after their author confirms", Access: AccessWrite,
		Approval: ApprovalAlways, Enabled: true, TimeoutSeconds: 60, MaxResultBytes: 256 * 1024,
	})
	result = append(result, Descriptor{
		ID: "runtime:publish_workspace_files", Kind: KindRuntimeFunction, Name: "publish_workspace_files",
		Description: "Publish an approved immutable workspace snapshot selection to project file versions", Access: AccessWrite,
		Approval: ApprovalAlways, Enabled: true, TimeoutSeconds: 120, MaxResultBytes: 512 * 1024,
	})
	result = append(result, Descriptor{
		ID: "runtime:publish_agent_memory", Kind: KindRuntimeFunction, Name: "publish_agent_memory",
		Description: "Replace private memory with an exact approved workspace snapshot selection", Access: AccessWrite,
		Approval: ApprovalAlways, Enabled: true, TimeoutSeconds: 120, MaxResultBytes: 64 * 1024,
	})
	result = append(result, Descriptor{
		ID: "runtime:install_workspace_skill", Kind: KindRuntimeFunction, Name: "install_workspace_skill",
		Description: "Install or upgrade a validated project Skill draft after user approval", Access: AccessWrite,
		Approval: ApprovalAlways, Enabled: true, TimeoutSeconds: 120, MaxResultBytes: 256 * 1024,
	})
	result = append(result, Descriptor{
		ID: "runtime:execute_skill_script", Kind: KindRuntimeFunction, Name: "execute_skill_script",
		Description: "Execute one declared Skill script inside the policy-gated OCI sandbox",
		Access:      AccessSensitive, Approval: ApprovalAlways, Enabled: true,
		TimeoutSeconds: 300, MaxResultBytes: 256 * 1024,
	})
	result = append(result, Descriptor{
		ID: "runtime:exec_command", Kind: KindRuntimeFunction, Name: "exec_command",
		Description: "Execute an approved SDK shell command in its policy-gated isolated workspace",
		Access:      AccessSensitive, Approval: ApprovalAlways, Enabled: true,
		TimeoutSeconds: 120, MaxResultBytes: 256 * 1024,
	})
	result = append(result, Descriptor{
		ID: "runtime:apply_patch", Kind: KindRuntimeFunction, Name: "apply_patch",
		Description: "Apply an approved native SDK multi-file patch in its isolated workspace",
		Access:      AccessWrite, Approval: ApprovalAlways, Enabled: true,
		TimeoutSeconds: 120, MaxResultBytes: 256 * 1024,
	})
	result = append(result, Descriptor{
		ID: "runtime:write_stdin", Kind: KindRuntimeFunction, Name: "write_stdin",
		Description: "Send explicitly approved input or a poll to the original SDK terminal; requires a bound native PTY provider",
		Access:      AccessSensitive, Approval: ApprovalAlways, Enabled: true,
		TimeoutSeconds: 120, MaxResultBytes: 256 * 1024,
	})
	return result
}

func validDependencyReference(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && identifierPattern.MatchString(parts[0]) && identifierPattern.MatchString(parts[1])
}

func validEnvironmentName(value string) bool {
	matched, _ := regexp.MatchString(`^[A-Za-z_][A-Za-z0-9_]*$`, strings.TrimSpace(value))
	return matched
}

func containsPath(paths []string, candidate string) bool {
	for _, item := range paths {
		if strings.EqualFold(filepath.Clean(item), filepath.Clean(candidate)) {
			return true
		}
	}
	return false
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func cloneMCPServer(source MCPServerConfig) MCPServerConfig {
	result := source
	result.WorkspaceIDs = slices.Clone(source.WorkspaceIDs)
	result.Args = slices.Clone(source.Args)
	result.AllowedTools = slices.Clone(source.AllowedTools)
	result.Environment = cloneMap(source.Environment)
	result.HeaderEnvironment = cloneMap(source.HeaderEnvironment)
	result.CredentialEnvironment = cloneMap(source.CredentialEnvironment)
	result.CredentialHeaders = cloneMap(source.CredentialHeaders)
	return result
}

func cloneMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("unexpected trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
