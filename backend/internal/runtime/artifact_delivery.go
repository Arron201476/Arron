package runtime

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode"
)

type ArtifactDownload struct {
	Format      string `json:"format"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	DownloadURL string `json:"download_url"`
}

type ArtifactDelivery struct {
	ProjectID         string             `json:"project_id"`
	ArtifactID        string             `json:"artifact_id"`
	ArtifactVersionID string             `json:"artifact_version_id"`
	Version           int                `json:"version"`
	Status            string             `json:"status"`
	Title             string             `json:"title"`
	Downloads         []ArtifactDownload `json:"downloads"`
	Warnings          []string           `json:"warnings"`
}

// Downloads are derived from an immutable version, not the artifact's current pointer.
func (s *Store) artifactDeliverySource(ctx context.Context, versionID string) (ArtifactDelivery, ArtifactVersion, map[string]any, error) {
	version, err := s.GetArtifactVersion(ctx, versionID)
	if err != nil {
		return ArtifactDelivery{}, version, nil, err
	}
	artifact, err := s.GetArtifact(ctx, version.ArtifactID)
	if err != nil {
		return ArtifactDelivery{}, version, nil, err
	}
	if _, err := projectFilesWorkspace(ctx, s.db, artifact.ProjectID, false); err != nil {
		return ArtifactDelivery{}, version, nil, err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(version.Payload))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return ArtifactDelivery{}, version, nil, domainError("REQUEST_VALIDATION_FAILED", "产物内容不是有效 JSON。")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return ArtifactDelivery{}, version, nil, domainError("REQUEST_VALIDATION_FAILED", "产物内容包含多个 JSON 值。")
	}
	payload, _ := value.(map[string]any)
	title, _ := payload["title"].(string)
	if strings.TrimSpace(title) == "" {
		title = artifact.ArtifactType
	}
	delivery := ArtifactDelivery{
		ProjectID: artifact.ProjectID, ArtifactID: artifact.ArtifactID,
		ArtifactVersionID: version.ArtifactVersionID, Version: version.Version,
		Status: version.Status, Title: title, Downloads: []ArtifactDownload{}, Warnings: []string{},
	}
	add := func(format, contentType string) {
		delivery.Downloads = append(delivery.Downloads, ArtifactDownload{
			Format: format, Filename: artifactDownloadFilename(title, version, format), ContentType: contentType,
			DownloadURL: "/api/v1/artifact-versions/" + url.PathEscape(versionID) + "/download?format=" + format,
		})
	}
	add("json", "application/json; charset=utf-8")
	if content, ok := artifactDocumentText(payload); ok {
		add("txt", "text/plain; charset=utf-8")
		add("md", "text/markdown; charset=utf-8")
		if artifactXMLText(content) {
			add("docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		} else {
			delivery.Warnings = append(delivery.Warnings, "正文包含 Word 不支持的控制字符，未提供 DOCX。")
		}
	}
	if _, err := artifactTableRecords(payload); err == nil {
		add("csv", "text/csv; charset=utf-8")
		delivery.Warnings = append(delivery.Warnings, "CSV 中可能被表格软件执行的公式以文本导出；完整原始数据保留在 JSON 中。")
	}
	if len(delivery.Downloads) == 1 {
		delivery.Warnings = append(delivery.Warnings, "未识别到唯一的文本正文或完整表格，仅提供完整 JSON。")
	}
	return delivery, version, payload, nil
}

func (s *Store) GetArtifactDelivery(ctx context.Context, versionID string) (ArtifactDelivery, error) {
	delivery, _, _, err := s.artifactDeliverySource(ctx, versionID)
	return delivery, err
}

func (s *Store) DownloadArtifactVersion(ctx context.Context, versionID, format string) (ArtifactDownload, []byte, error) {
	delivery, version, payload, err := s.artifactDeliverySource(ctx, versionID)
	if err != nil {
		return ArtifactDownload{}, nil, err
	}
	var download ArtifactDownload
	for _, item := range delivery.Downloads {
		if item.Format == format {
			download = item
			break
		}
	}
	if download.Format == "" {
		return ArtifactDownload{}, nil, domainError("REQUEST_VALIDATION_FAILED", "当前产物版本不支持该下载格式。")
	}
	switch format {
	case "json":
		return download, bytes.Clone(version.Payload), nil
	case "txt", "md":
		content, _ := artifactDocumentText(payload)
		return download, []byte(content), nil
	case "docx":
		content, _ := artifactDocumentText(payload)
		data, err := buildDOCX(strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n"))
		return download, data, err
	case "csv":
		records, err := artifactTableRecords(payload)
		if err != nil {
			return ArtifactDownload{}, nil, err
		}
		var buffer bytes.Buffer
		buffer.WriteString("\xef\xbb\xbf")
		writer := csv.NewWriter(&buffer)
		for _, record := range records {
			for index, cell := range record {
				record[index] = artifactCSVText(cell)
			}
			if err := writer.Write(record); err != nil {
				return ArtifactDownload{}, nil, err
			}
		}
		writer.Flush()
		return download, buffer.Bytes(), writer.Error()
	}
	return ArtifactDownload{}, nil, domainError("REQUEST_VALIDATION_FAILED", "不支持的下载格式。")
}

func artifactDocumentText(payload map[string]any) (string, bool) {
	content, ok := payload["content_markdown"].(string)
	if !ok {
		content, ok = payload["content"].(string)
	}
	if script, hasScript := payload["script_text"].(string); hasScript {
		if ok && content != script {
			return "", false
		}
		return script, true
	}
	return content, ok
}

func artifactDownloadFilename(title string, version ArtifactVersion, format string) string {
	clean := strings.Map(func(char rune) rune {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) || strings.ContainsRune(`/\:*?"<>|`, char) {
			return '_'
		}
		return char
	}, title)
	runes := []rune(strings.Trim(clean, " ."))
	if len(runes) > 60 {
		runes = runes[:60]
	}
	return fmt.Sprintf("artifact-%s-v%d-%s.%s", string(runes), version.Version, version.ArtifactVersionID, format)
}

func artifactXMLText(value string) bool {
	for _, char := range value {
		if char != '\t' && char != '\n' && char != '\r' && !(char >= 0x20 && char <= 0xd7ff || char >= 0xe000 && char <= 0xfffd || char >= 0x10000 && char <= 0x10ffff) {
			return false
		}
	}
	return true
}

func artifactCSVText(value string) string {
	trimmed := strings.TrimLeftFunc(value, func(char rune) bool {
		return unicode.IsSpace(char) || unicode.IsControl(char) || unicode.Is(unicode.Cf, char)
	})
	if strings.ContainsAny(value, "\t\r\n") || trimmed != "" && strings.ContainsRune("=+-@＝＋－＠", []rune(trimmed)[0]) {
		return "'" + value
	}
	return value
}

// Accept explicit columns and rectangular rows only; never drop unknown object fields.
func artifactTableRecords(payload map[string]any) ([][]string, error) {
	invalid := func() ([][]string, error) {
		return nil, domainError("REQUEST_VALIDATION_FAILED", "表格列或行结构不完整。")
	}
	columns, ok := payload["columns"].([]any)
	rows, rowsOK := payload["rows"].([]any)
	if !ok || !rowsOK || len(columns) == 0 || len(columns) > 1000 || len(rows) > 100000/len(columns) {
		return invalid()
	}
	keys, headers := make([]string, len(columns)), make([]string, len(columns))
	seen := make(map[string]bool)
	for index, column := range columns {
		key, label := "", ""
		switch column := column.(type) {
		case string:
			key, label = column, column
		case map[string]any:
			key, _ = column["key"].(string)
			label, _ = column["label"].(string)
			if label == "" {
				label = key
			}
		default:
			return invalid()
		}
		if strings.TrimSpace(key) == "" || seen[key] {
			return invalid()
		}
		seen[key], keys[index], headers[index] = true, key, label
	}
	records := [][]string{headers}
	for _, row := range rows {
		values := make([]any, len(columns))
		switch row := row.(type) {
		case []any:
			if len(row) != len(columns) {
				return invalid()
			}
			values = row
		case map[string]any:
			for key := range row {
				if !seen[key] {
					return invalid()
				}
			}
			for index, key := range keys {
				values[index] = row[key]
			}
		default:
			return invalid()
		}
		record := make([]string, len(values))
		for index, value := range values {
			switch value := value.(type) {
			case nil:
			case string:
				record[index] = value
			case json.Number:
				record[index] = value.String()
			case bool:
				record[index] = fmt.Sprint(value)
			default:
				return invalid()
			}
		}
		records = append(records, record)
	}
	return records, nil
}
