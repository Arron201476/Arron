package agenttool

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
	"strings"
)

var credentialHeaderName = regexp.MustCompile("^[A-Za-z][A-Za-z0-9-]{0,63}$")

func validateMCPCredentialTargets(server MCPServerConfig) error {
	if server.CredentialBinding != "" {
		return errors.New("credential_binding is resolved per execution, not an operator configuration field")
	}
	if len(server.CredentialEnvironment)+len(server.CredentialHeaders) > 16 {
		return errors.New("MCP credential fields exceed the limit")
	}
	seen := map[string]bool{}
	for target, field := range server.CredentialEnvironment {
		upper := strings.ToUpper(target)
		if !validEnvironmentName(target) || strings.TrimSpace(target) != target || !identifierPattern.MatchString(field) || seen[upper] {
			return errors.New("invalid MCP credential environment target or field")
		}
		// User values must not become interpreter flags, import paths or loader hooks.
		if slices.Contains([]string{"PATH", "PATHEXT", "COMSPEC", "SYSTEMROOT", "WINDIR", "HOME", "USERPROFILE", "BASH_ENV", "ENV", "NODE_OPTIONS", "RUBYOPT", "PERL5OPT", "CLASSPATH", "JAVA_TOOL_OPTIONS"}, upper) ||
			strings.HasPrefix(upper, "PYTHON") || strings.HasPrefix(upper, "LD_") || strings.HasPrefix(upper, "DYLD_") {
			return errors.New("MCP credentials cannot configure process execution")
		}
		for name := range server.Environment {
			if strings.EqualFold(name, target) {
				return errors.New("MCP credential target overlaps operator environment")
			}
		}
		seen[upper] = true
	}
	for target, field := range server.CredentialHeaders {
		lower := strings.ToLower(target)
		if !credentialHeaderName.MatchString(target) || !identifierPattern.MatchString(field) || seen[lower] ||
			slices.Contains([]string{"host", "content-length", "transfer-encoding", "connection", "proxy-authorization", "proxy-connection", "upgrade"}, lower) {
			return errors.New("invalid MCP credential header target or field")
		}
		for name := range server.HeaderEnvironment {
			if strings.EqualFold(name, target) {
				return errors.New("MCP credential target overlaps operator headers")
			}
		}
		seen[lower] = true
	}
	return nil
}

func (server MCPServerConfig) CredentialFields() []string {
	fields := []string{}
	for _, mapping := range []map[string]string{server.CredentialEnvironment, server.CredentialHeaders} {
		for _, name := range mapping {
			if !slices.Contains(fields, name) {
				fields = append(fields, name)
			}
		}
	}
	slices.Sort(fields)
	return fields
}

// Call only on a fresh workspace registry. Bind identifiers/revisions, never secrets.
func (r *Registry) BindMCPCredentials(bindings map[string]string, unavailable map[string]bool) {
	for index := range r.config.MCPServers {
		server := &r.config.MCPServers[index]
		binding := bindings[server.ID]
		if binding == "" || len(server.CredentialFields()) == 0 {
			continue
		}
		server.CredentialBinding = binding
		if unavailable[server.ID] {
			server.Enabled = false
		}
		r.mcpServers[server.ID] = cloneMCPServer(*server)
		for index := range r.descriptors {
			descriptor := &r.descriptors[index]
			if descriptor.Kind != KindMCP || descriptor.ServerID != server.ID {
				continue
			}
			digest := sha256.Sum256([]byte(descriptor.ConfigurationHash + "\n" + binding))
			descriptor.ConfigurationHash = "sha256:" + hex.EncodeToString(digest[:])
			if unavailable[server.ID] {
				descriptor.Enabled = false
			}
			r.mcpTools[server.ID+"/"+descriptor.Name] = *descriptor
		}
	}
}
