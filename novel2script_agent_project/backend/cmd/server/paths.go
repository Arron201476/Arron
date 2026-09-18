package main

import (
	"os"
	"path/filepath"
	"strings"
)

func stateDirectory() string {
	if configured := strings.TrimSpace(os.Getenv("N2S_TRACE_DIR")); configured != "" {
		return configured
	}
	cwd, err := os.Getwd()
	if err == nil {
		if strings.EqualFold(filepath.Base(cwd), "backend") {
			return filepath.Join(filepath.Dir(cwd), "runs")
		}
		return filepath.Join(cwd, "runs")
	}
	if executable, executableErr := os.Executable(); executableErr == nil {
		return filepath.Join(filepath.Dir(executable), "runs")
	}
	return "runs"
}

func webDirectory() string {
	if configured := strings.TrimSpace(os.Getenv("N2S_WEB_DIR")); configured != "" {
		return configured
	}
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	candidate := filepath.Join(filepath.Dir(executable), "web")
	if info, statErr := os.Stat(filepath.Join(candidate, "index.html")); statErr == nil && !info.IsDir() {
		return candidate
	}
	return ""
}
