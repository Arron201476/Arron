package main

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
)

const shutdownTokenHeader = "X-N2S-Shutdown-Token"

func (s *apiServer) shutdownService(w http.ResponseWriter, r *http.Request) {
	configured := strings.TrimSpace(s.shutdownToken)
	provided := strings.TrimSpace(r.Header.Get(shutdownTokenHeader))
	if configured == "" || subtle.ConstantTimeCompare([]byte(configured), []byte(provided)) != 1 || s.shutdown == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.shutdown()
	}()
}
