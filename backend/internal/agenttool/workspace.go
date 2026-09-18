package agenttool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
)

// Workspace settings select trusted operator definitions; they cannot introduce
// host commands, network endpoints, environment names or vector store grants.
type WorkspaceSettings struct {
	Enabled  map[string]bool `json:"enabled"`
	Revision int             `json:"-"`
}

type ConfigurationOption struct {
	ID           string `json:"id"`
	Kind         Kind   `json:"kind"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Transport    string `json:"transport,omitempty"`
	Enabled      bool   `json:"enabled"`
	Configurable bool   `json:"configurable"`
	UserMessage  string `json:"user_message,omitempty"`
}

func nativeHostedDefaults() []HostedToolConfig {
	return []HostedToolConfig{
		{ID: "native-web-search", Type: "web_search", Description: "Web search", Access: AccessRead},
		{ID: "native-file-search", Type: "file_search", Description: "File search", Access: AccessRead},
		{ID: "native-code-interpreter", Type: "code_interpreter", Description: "Code interpreter", Access: AccessWrite, Approval: ApprovalAlways},
		{ID: "native-image-generation", Type: "image_generation", Description: "Image generation", Access: AccessWrite, Approval: ApprovalAlways},
	}
}

func visibleInWorkspace(workspaceIDs []string, workspaceID string) bool {
	return len(workspaceIDs) == 0 || slices.Contains(workspaceIDs, workspaceID)
}

func (r *Registry) ForWorkspace(workspaceID string, settings WorkspaceSettings) (*Registry, error) {
	config := Config{SchemaVersion: SchemaVersion, StdioPolicy: r.config.StdioPolicy}
	config.Programmatic = cloneProgrammaticConfig(r.config.Programmatic)
	if !visibleInWorkspace(config.Programmatic.WorkspaceIDs, workspaceID) {
		config.Programmatic.Enabled = false
		config.Programmatic.ToolIDs = nil
	} else if enabled, exists := settings.Enabled[ProgrammaticOptionID]; exists {
		config.Programmatic.Enabled = enabled && len(config.Programmatic.ToolIDs) > 0
	}
	config.StdioPolicy.AllowedCommands = slices.Clone(r.config.StdioPolicy.AllowedCommands)
	config.StdioPolicy.AllowedCwds = slices.Clone(r.config.StdioPolicy.AllowedCwds)
	for _, original := range r.config.MCPServers {
		if !visibleInWorkspace(original.WorkspaceIDs, workspaceID) {
			continue
		}
		server := cloneMCPServer(original)
		id := "mcp:" + server.ID
		if enabled, ok := settings.Enabled[id]; ok {
			server.Enabled = enabled
		}
		config.MCPServers = append(config.MCPServers, server)
	}
	hosted := cloneHostedTools(r.config.HostedTools)
	for _, fallback := range nativeHostedDefaults() {
		exists := slices.ContainsFunc(hosted, func(tool HostedToolConfig) bool { return tool.ID == fallback.ID })
		if !exists {
			hosted = append(hosted, fallback)
		}
	}
	for _, tool := range hosted {
		if !visibleInWorkspace(tool.WorkspaceIDs, workspaceID) {
			continue
		}
		id := "hosted:" + tool.ID
		if enabled, ok := settings.Enabled[id]; ok {
			tool.Enabled = enabled
		}
		config.HostedTools = append(config.HostedTools, tool)
	}
	registry, err := Compile(config)
	if err != nil {
		return nil, err
	}
	if settings.Revision > 0 {
		for index := range registry.descriptors {
			descriptor := &registry.descriptors[index]
			if descriptor.ConfigurationHash == "" {
				continue
			}
			hash := sha256.Sum256([]byte(workspaceID + "\n" + strconv.Itoa(settings.Revision) + "\n" + descriptor.ConfigurationHash))
			descriptor.ConfigurationHash = "sha256:" + hex.EncodeToString(hash[:])
			if descriptor.Kind == KindMCP {
				registry.mcpTools[descriptor.ServerID+"/"+descriptor.Name] = *descriptor
			}
		}
	}
	return registry, nil
}

func (r *Registry) ConfigurationOptions() []ConfigurationOption {
	options := make([]ConfigurationOption, 0, len(r.config.MCPServers)+len(r.config.HostedTools))
	programmatic := ConfigurationOption{ID: ProgrammaticOptionID, Kind: KindHosted,
		Name: "programmatic_tool_calling", Description: "PTC", Enabled: r.config.Programmatic.Enabled,
		Configurable: len(r.config.Programmatic.ToolIDs) > 0}
	if !programmatic.Configurable {
		programmatic.UserMessage = "当前工作区未配置获准的程序调用工具。"
	}
	options = append(options, programmatic)
	for _, server := range r.config.MCPServers {
		options = append(options, ConfigurationOption{ID: "mcp:" + server.ID, Kind: KindMCP, Name: server.ID,
			Description: server.Description, Transport: server.Transport, Enabled: server.Enabled, Configurable: true})
	}
	for _, tool := range r.config.HostedTools {
		option := ConfigurationOption{ID: "hosted:" + tool.ID, Kind: KindHosted, Name: tool.Type,
			Description: tool.Description, Enabled: tool.Enabled, Configurable: true}
		if tool.Type == "file_search" && len(tool.VectorStoreIDs) == 0 {
			option.Configurable = false
			option.UserMessage = "需要先由平台管理员为该工作区配置可访问的向量库。"
		}
		options = append(options, option)
	}
	return options
}

func (r *Registry) bindConfigurationHashes() {
	for index := range r.descriptors {
		descriptor := &r.descriptors[index]
		var config any
		switch descriptor.Kind {
		case KindRuntimeFunction:
			if !slices.Contains(r.programmaticToolIDs(), descriptor.ID) {
				continue
			}
			config = r.config.Programmatic
		case KindMCP:
			config = r.mcpServers[descriptor.ServerID]
		case KindHosted:
			for _, tool := range r.config.HostedTools {
				if "hosted:"+tool.ID == descriptor.ID {
					config = tool
					break
				}
			}
		default:
			continue
		}
		data, _ := json.Marshal(config)
		hash := sha256.Sum256(data)
		descriptor.ConfigurationHash = "sha256:" + hex.EncodeToString(hash[:])
		if descriptor.Kind == KindMCP {
			r.mcpTools[descriptor.ServerID+"/"+descriptor.Name] = *descriptor
		}
	}
}
