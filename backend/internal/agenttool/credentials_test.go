package agenttool

import (
	"encoding/json"
	"strings"
	"testing"
)

func credentialTestConfig() Config {
	return Config{SchemaVersion: SchemaVersion, MCPServers: []MCPServerConfig{{
		ID: "service", Description: "Fixture service", Transport: "streamable_http", URL: "https://example.com/mcp", Enabled: true,
		CredentialHeaders: map[string]string{"Authorization": "api-token"},
		AllowedTools:      []MCPToolConfig{{Name: "read", Description: "Read", Access: AccessRead}},
	}}}
}

func TestMCPCredentialBindingChangesApprovalIdentityWithoutPublicDisclosure(t *testing.T) {
	operator, err := Compile(credentialTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, binding := range []string{"user-a:version-1", "user-a:version-2", "user-b:version-1"} {
		workspace, err := operator.ForWorkspace("workspace", WorkspaceSettings{Revision: 4})
		if err != nil {
			t.Fatal(err)
		}
		before, _ := workspace.Get("mcp:service/read")
		workspace.BindMCPCredentials(map[string]string{"service": binding}, nil)
		after, _ := workspace.Get(before.ID)
		if after.ConfigurationHash == before.ConfigurationHash {
			t.Fatal("credential version not bound to approval")
		}
		hashes = append(hashes, after.ConfigurationHash)
		private := workspace.PrivateCatalog().MCPServers[0]
		if private.CredentialBinding != binding || len(private.CredentialFields()) != 1 {
			t.Fatal("missing private connection binding")
		}
		private.CredentialHeaders["Authorization"] = "mutated"
		if workspace.PrivateCatalog().MCPServers[0].CredentialHeaders["Authorization"] != "api-token" {
			t.Fatal("mutable catalog changed trusted slots")
		}
		public, _ := json.Marshal(workspace.PublicCatalog())
		if strings.Contains(string(public), binding) || strings.Contains(string(public), "Authorization") {
			t.Fatal("public catalog leaked credential configuration")
		}
	}
	if hashes[0] == hashes[1] || hashes[0] == hashes[2] {
		t.Fatal("different credential identities share an approval")
	}
	if operator.PrivateCatalog().MCPServers[0].CredentialBinding != "" {
		t.Fatal("execution mutated operator configuration")
	}
}

func TestMCPCredentialTargetsRejectUnsafeOrAmbiguousBindings(t *testing.T) {
	for _, name := range []string{"Host", "Content-Length", "Transfer-Encoding", "Proxy-Authorization", "X-Test\r\nBad", " Authorization"} {
		t.Run(name, func(t *testing.T) {
			config := credentialTestConfig()
			config.MCPServers[0].CredentialHeaders = map[string]string{name: "token"}
			if _, err := Compile(config); err == nil {
				t.Fatal("unsafe header accepted")
			}
		})
	}
	for _, mutate := range []func(*MCPServerConfig){
		func(server *MCPServerConfig) { server.CredentialHeaders["authorization"] = "another" },
		func(server *MCPServerConfig) {
			server.HeaderEnvironment = map[string]string{"authorization": "SERVICE_TOKEN"}
		},
		func(server *MCPServerConfig) { server.CredentialHeaders["Authorization"] = "env://SECRET" },
		func(server *MCPServerConfig) { server.CredentialBinding = "forged" },
		func(server *MCPServerConfig) { server.CredentialEnvironment = map[string]string{"API_TOKEN": "token"} },
	} {
		config := credentialTestConfig()
		mutate(&config.MCPServers[0])
		if _, err := Compile(config); err == nil {
			t.Fatal("ambiguous credential configuration accepted")
		}
	}
}

func TestMCPCredentialEnvironmentDoesNotAcceptExecutionControls(t *testing.T) {
	for _, name := range []string{"Path", "PYTHONPATH", "PYTHONSTARTUP", "LD_PRELOAD", "NODE_OPTIONS", "BASH_ENV", "JAVA_TOOL_OPTIONS", "BAD\nNAME"} {
		if err := validateMCPCredentialTargets(MCPServerConfig{CredentialEnvironment: map[string]string{name: "token"}}); err == nil {
			t.Fatalf("unsafe process field %q", name)
		}
	}
	if err := validateMCPCredentialTargets(MCPServerConfig{CredentialEnvironment: map[string]string{"SERVICE_API_TOKEN": "token"}}); err != nil {
		t.Fatal(err)
	}
}
