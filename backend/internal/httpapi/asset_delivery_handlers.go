package httpapi

import (
	"mime"
	"net/http"
	"strings"
	"unicode"
)

func (s *Server) downloadAsset(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "private, no-store")
	content, err := s.runtime.OpenAssetContent(request.Context(), request.PathValue("asset_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	defer content.File.Close()
	filename := strings.Map(func(char rune) rune {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) || strings.ContainsRune(`/\:*?"<>|`, char) {
			return '_'
		}
		return char
	}, content.Asset.OriginalFilename)
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(writer, request, filename, content.Asset.UpdatedAt, content.File)
}
