package runtime

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

type AgentToolCitation struct {
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	URL      string `json:"url,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	Filename string `json:"filename,omitempty"`
}

type toolCitationEnvelope struct {
	Version   string              `json:"version"`
	Citations []AgentToolCitation `json:"citations"`
	Omitted   int                 `json:"omitted"`
}

var citationFileID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// Preserve bounded native annotations separately from the lossy diagnostic summary.
func hostedCitationSummary(raw, summary json.RawMessage) json.RawMessage {
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = json.RawMessage(encoded)
	}
	var output struct {
		Citations []json.RawMessage `json:"citations"`
	}
	if json.Unmarshal(raw, &output) != nil || len(output.Citations) == 0 {
		return summary
	}
	// Do not retain rejected citation URLs inside the generic summary either.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil {
		delete(fields, "citations")
		data, _ := json.Marshal(fields)
		if cleaned, _, _, err := summarizeAgentToolPayload(data, false); err == nil {
			summary = cleaned
		}
	}
	metadata := toolCitationEnvelope{Version: "tool_citations.v1", Citations: []AgentToolCitation{}}
	seen, size := map[string]bool{}, 0
	for _, rawCitation := range output.Citations {
		var citation AgentToolCitation
		if json.Unmarshal(rawCitation, &citation) != nil {
			metadata.Omitted++
			continue
		}
		citation.Title, citation.Filename = citationLabel(citation.Title), citationLabel(citation.Filename)
		switch citation.Type {
		case "url_citation":
			if !safeCitationURL(citation.URL) {
				metadata.Omitted++
				continue
			}
			citation.FileID, citation.Filename = "", ""
		case "file_citation":
			if !citationFileID.MatchString(citation.FileID) {
				metadata.Omitted++
				continue
			}
			citation.URL, citation.Title = "", ""
		default:
			metadata.Omitted++
			continue
		}
		key := citation.Type + ":" + citation.URL + citation.FileID
		if seen[key] {
			continue
		}
		data, _ := json.Marshal(citation)
		if len(metadata.Citations) >= 32 || size+len(data) > 8*1024 {
			metadata.Omitted++
			continue
		}
		seen[key] = true
		size += len(data)
		metadata.Citations = append(metadata.Citations, citation)
	}
	result, _ := json.Marshal(struct {
		Summary  json.RawMessage      `json:"summary"`
		Delivery toolCitationEnvelope `json:"delivery"`
	}{summary, metadata})
	if len(result) > maxAgentToolSummaryBytes {
		result, _ = json.Marshal(struct {
			Summary  json.RawMessage      `json:"summary"`
			Delivery toolCitationEnvelope `json:"delivery"`
		}{json.RawMessage(`{"truncated":true}`), metadata})
	}
	return result
}

func citationLabel(value string) string {
	return truncateUTF8(strings.Map(func(char rune) rune {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) {
			return -1
		}
		return char
	}, value), 256)
}

func safeCitationURL(value string) bool {
	if len(value) == 0 || len(value) > 2048 {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil {
		return false
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return false
	}
	fragment, _ := url.ParseQuery(parsed.Fragment)
	for key := range fragment {
		if isSensitiveAgentToolKey(key) {
			return false
		}
	}
	for key := range query {
		if isSensitiveAgentToolKey(key) {
			return false
		}
	}
	for _, char := range value {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) || char == '\\' {
			return false
		}
	}
	return true
}

func attachToolCitations(call *AgentToolCall) {
	if call.ToolKind != "hosted" {
		return
	}
	var payload struct {
		Delivery toolCitationEnvelope `json:"delivery"`
	}
	if json.Unmarshal(call.ResultSummary, &payload) == nil && payload.Delivery.Version == "tool_citations.v1" {
		call.Citations, call.OmittedCitations = payload.Delivery.Citations, payload.Delivery.Omitted
	}
}
