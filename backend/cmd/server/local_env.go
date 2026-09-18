package main

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
)

// loadLocalEnvironment keeps the standalone development binary aligned with
// the checked-in launcher. Explicit process variables retain precedence.
func loadLocalEnvironment(projectRoot string) error {
	path := filepath.Join(projectRoot, "backend", ".env.local")
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !found || !validEnvironmentName(name) {
			return fmt.Errorf("invalid environment entry at %s:%d", path, lineNumber)
		}
		if _, present := os.LookupEnv(name); present {
			continue
		}
		if err := os.Setenv(name, strings.TrimSpace(value)); err != nil {
			return fmt.Errorf("set %s: %w", name, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func configureInternalEndpointProxyBypass() {
	hosts := []string{"127.0.0.1", "localhost"}
	for _, name := range []string{
		"CONTENT_AGENT_CONTROL_MODEL_ENDPOINT",
		"CONTENT_AGENT_CONTENT_MODEL_ENDPOINT",
		"CONTENT_AGENT_VIDEO_MODEL_ENDPOINT",
		"CONTENT_AGENT_IMAGE_MODEL_ENDPOINT",
		"CONTENT_AGENT_MEDIAKIT_ENDPOINT",
	} {
		parsed, err := url.Parse(strings.TrimSpace(os.Getenv(name)))
		if err != nil {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if host == "internal.example.com" || strings.HasSuffix(host, ".internal.example.com") ||
			name == "CONTENT_AGENT_MEDIAKIT_ENDPOINT" {
			hosts = append(hosts, host)
		}
	}
	if strings.TrimSpace(os.Getenv("CONTENT_AGENT_MEDIAKIT_ENDPOINT")) != "" {
		hosts = append(hosts, ".volcvod.com")
	}
	for _, name := range []string{"NO_PROXY", "no_proxy"} {
		entries := strings.Split(os.Getenv(name), ",")
		seen := make(map[string]bool, len(entries)+len(hosts))
		merged := make([]string, 0, len(entries)+len(hosts))
		for _, entry := range append(entries, hosts...) {
			entry = strings.TrimSpace(entry)
			key := strings.ToLower(entry)
			if entry == "" || seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, entry)
		}
		_ = os.Setenv(name, strings.Join(merged, ","))
	}
}

func validEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for index, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '_' ||
			(index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}

func configureBundledMediaTools(projectRoot string) {
	tools := map[string]string{
		"CONTENT_AGENT_FFMPEG_COMMAND": filepath.Join(
			projectRoot, ".tools", "ffmpeg-package", "ffmpeg-8.1.2-essentials_build", "bin", "ffmpeg.exe",
		),
		"CONTENT_AGENT_FFPROBE_COMMAND": filepath.Join(
			projectRoot, ".tools", "ffmpeg-package", "ffmpeg-8.1.2-essentials_build", "bin", "ffprobe.exe",
		),
	}
	for name, path := range tools {
		if _, present := os.LookupEnv(name); present {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			_ = os.Setenv(name, path)
		}
	}
	if goruntime.GOOS == "windows" {
		wrapperPath := filepath.Join(projectRoot, "scripts", "invoke-media-command.ps1")
		if _, present := os.LookupEnv("CONTENT_AGENT_MEDIA_COMMAND_WRAPPER"); !present {
			if info, err := os.Stat(wrapperPath); err == nil && !info.IsDir() {
				_ = os.Setenv("CONTENT_AGENT_MEDIA_COMMAND_WRAPPER", wrapperPath)
			}
		}
	}
}
