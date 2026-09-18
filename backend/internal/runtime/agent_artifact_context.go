package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"content-agent/backend/internal/agentcontract"
)

const (
	maxFocusedArtifacts = 12
	maxFocusedBytes     = 48 * 1024
	maxArtifactBytes    = 16 * 1024
	maxIndexedScopes    = 20
)

type agentArtifactRow struct {
	ArtifactID        string
	ArtifactVersionID string
	RunID             string
	CapabilityID      string
	ArtifactType      string
	ScopeKey          string
	Status            string
	Version           int
	Payload           json.RawMessage
	UpdatedAt         string
}

func (s *Store) loadAgentArtifactContext(
	ctx context.Context,
	projectID string,
	request *agentcontract.MessageRequest,
) ([]agentcontract.ArtifactSetContext, []agentcontract.FocusedArtifactContext, string, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.artifact_id, a.current_version_id, COALESCE(a.run_id, ''), a.capability_id,
			a.artifact_type, a.scope_key, av.status, av.version, av.payload_json,
			a.updated_at
		FROM artifacts a
		JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
		WHERE a.project_id = ?
		ORDER BY a.updated_at DESC, a.artifact_id ASC`, projectID)
	if err != nil {
		return nil, nil, "", false, err
	}
	defer rows.Close()

	artifacts := make([]agentArtifactRow, 0)
	sets := make([]agentcontract.ArtifactSetContext, 0)
	setIndexes := make(map[string]int)
	for rows.Next() {
		var artifact agentArtifactRow
		var payload string
		if err := rows.Scan(
			&artifact.ArtifactID, &artifact.ArtifactVersionID, &artifact.RunID,
			&artifact.CapabilityID, &artifact.ArtifactType, &artifact.ScopeKey,
			&artifact.Status, &artifact.Version, &payload, &artifact.UpdatedAt,
		); err != nil {
			return nil, nil, "", false, err
		}
		artifact.Payload = json.RawMessage(payload)
		artifacts = append(artifacts, artifact)
		displayLabel := artifactDisplayLabel(artifact.ArtifactType, artifact.Payload)

		key := strings.Join([]string{
			artifact.RunID, artifact.CapabilityID, artifact.ArtifactType, artifact.Status,
		}, "\x00")
		if artifact.ArtifactType == "generic_document" || artifact.ArtifactType == "generic_table" {
			key += "\x00" + artifact.ArtifactID
		}
		index, ok := setIndexes[key]
		if !ok {
			index = len(sets)
			setIndexes[key] = index
			sets = append(sets, agentcontract.ArtifactSetContext{
				RunID: artifact.RunID, CapabilityID: artifact.CapabilityID,
				ArtifactType: artifact.ArtifactType, ArtifactLabel: displayLabel,
				Status: artifact.Status, ScopeKeys: []string{}, UpdatedAt: artifact.UpdatedAt,
			})
		}
		sets[index].Count++
		if len(sets[index].ScopeKeys) < maxIndexedScopes {
			sets[index].ScopeKeys = append(sets[index].ScopeKeys, artifact.ScopeKey)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, "", false, err
	}
	if request == nil || len(artifacts) == 0 {
		return sets, []agentcontract.FocusedArtifactContext{}, "", false, nil
	}

	selected, focus, err := s.resolveFocusedAgentArtifacts(ctx, projectID, *request, artifacts)
	if err != nil {
		return nil, nil, "", false, err
	}
	if requestsArtifactRegeneration(strings.ToLower(request.Content)) {
		focused, truncated := materializeArtifactReferences(selected)
		return sets, focused, focus, truncated, nil
	}
	focused, truncated := materializeFocusedArtifacts(selected)
	return sets, focused, focus, truncated, nil
}

func (s *Store) resolveFocusedAgentArtifacts(
	ctx context.Context,
	projectID string,
	request agentcontract.MessageRequest,
	artifacts []agentArtifactRow,
) ([]agentArtifactRow, string, error) {
	if request.SelectionSnapshot != nil && request.SelectionSnapshot.ArtifactVersionID != "" {
		artifact, err := s.loadAgentArtifactVersion(ctx, projectID, request.SelectionSnapshot.ArtifactVersionID)
		if err != nil {
			return nil, "", err
		}
		return []agentArtifactRow{artifact}, "explicit_selection", nil
	}

	if candidates := filterAgentArtifactsByDynamicLabel(artifacts, request.Content); len(candidates) > 0 {
		preferredRunID := firstStringPointerValue(
			request.ClientContext.ViewedRunID,
			request.ClientContext.CurrentRunID,
		)
		if request.ClientContext.CurrentArtifactID != nil {
			for _, candidate := range candidates {
				if candidate.ArtifactID == *request.ClientContext.CurrentArtifactID {
					return []agentArtifactRow{candidate}, "semantic_reference", nil
				}
			}
		}
		if preferredRunID != "" {
			for _, candidate := range candidates {
				if candidate.RunID == preferredRunID {
					return []agentArtifactRow{candidate}, "semantic_reference", nil
				}
			}
		}
		if len(candidates) == 1 {
			return candidates, "semantic_reference", nil
		}
	}

	wantedTypes := referencedArtifactTypes(request.Content)
	if len(wantedTypes) > 0 {
		candidates := filterAgentArtifactsByType(artifacts, wantedTypes)
		if len(candidates) > 0 {
			preferredRunID := firstStringPointerValue(
				request.ClientContext.ViewedRunID,
				request.ClientContext.CurrentRunID,
			)
			selectedRunID := candidates[0].RunID
			if preferredRunID != "" {
				for _, candidate := range candidates {
					if candidate.RunID == preferredRunID {
						selectedRunID = preferredRunID
						break
					}
				}
			}
			selected := make([]agentArtifactRow, 0, len(candidates))
			for _, candidate := range candidates {
				if candidate.RunID == selectedRunID {
					selected = append(selected, candidate)
				}
			}
			sortAgentArtifacts(selected)
			return selected, "semantic_reference", nil
		}
	}

	if request.ClientContext.CurrentArtifactVersionID != nil && *request.ClientContext.CurrentArtifactVersionID != "" {
		artifact, err := s.loadAgentArtifactVersion(ctx, projectID, *request.ClientContext.CurrentArtifactVersionID)
		if err == nil {
			return []agentArtifactRow{artifact}, "current_view", nil
		}
		if !isDomainErrorCode(err, "ARTIFACT_VERSION_NOT_FOUND") {
			return nil, "", err
		}
	}
	if request.ClientContext.CurrentArtifactID != nil {
		for _, artifact := range artifacts {
			if artifact.ArtifactID == *request.ClientContext.CurrentArtifactID {
				return []agentArtifactRow{artifact}, "current_view", nil
			}
		}
	}
	return nil, "", nil
}

func (s *Store) loadAgentArtifactVersion(
	ctx context.Context,
	projectID string,
	artifactVersionID string,
) (agentArtifactRow, error) {
	var artifact agentArtifactRow
	var payload string
	err := s.db.QueryRowContext(ctx, `
		SELECT a.artifact_id, av.artifact_version_id, COALESCE(a.run_id, ''), a.capability_id,
			a.artifact_type, a.scope_key, av.status, av.version, av.payload_json,
			a.updated_at
		FROM artifact_versions av
		JOIN artifacts a ON a.artifact_id = av.artifact_id
		WHERE av.artifact_version_id = ? AND a.project_id = ?`,
		artifactVersionID, projectID,
	).Scan(
		&artifact.ArtifactID, &artifact.ArtifactVersionID, &artifact.RunID,
		&artifact.CapabilityID, &artifact.ArtifactType, &artifact.ScopeKey,
		&artifact.Status, &artifact.Version, &payload, &artifact.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return agentArtifactRow{}, domainError("ARTIFACT_VERSION_NOT_FOUND", "产物版本不存在。")
	}
	if err != nil {
		return agentArtifactRow{}, err
	}
	artifact.Payload = json.RawMessage(payload)
	return artifact, nil
}

func referencedArtifactTypes(content string) map[string]struct{} {
	normalized := strings.ToLower(strings.TrimSpace(content))
	type aliasMatch struct {
		artifactType string
		alias        string
	}
	matches := make([]aliasMatch, 0)
	for artifactType, aliases := range artifactTargetAliases {
		for _, alias := range aliases {
			alias = strings.ToLower(alias)
			if strings.Contains(normalized, alias) {
				matches = append(matches, aliasMatch{artifactType: artifactType, alias: alias})
			}
		}
	}
	result := make(map[string]struct{})
	for i, match := range matches {
		shadowed := false
		for j, other := range matches {
			if i != j && len(other.alias) > len(match.alias) && strings.Contains(other.alias, match.alias) {
				shadowed = true
				break
			}
		}
		if !shadowed {
			result[match.artifactType] = struct{}{}
		}
	}
	return result
}

func filterAgentArtifactsByType(
	artifacts []agentArtifactRow,
	wanted map[string]struct{},
) []agentArtifactRow {
	result := make([]agentArtifactRow, 0)
	for _, artifact := range artifacts {
		if _, ok := wanted[artifact.ArtifactType]; ok {
			result = append(result, artifact)
		}
	}
	return result
}

func sortAgentArtifacts(artifacts []agentArtifactRow) {
	sort.SliceStable(artifacts, func(i, j int) bool {
		if artifacts[i].ArtifactType != artifacts[j].ArtifactType {
			return artifacts[i].ArtifactType < artifacts[j].ArtifactType
		}
		iEpisode := revisionEpisodeFromScope(artifacts[i].ScopeKey)
		jEpisode := revisionEpisodeFromScope(artifacts[j].ScopeKey)
		if iEpisode > 0 && jEpisode > 0 && iEpisode != jEpisode {
			return iEpisode < jEpisode
		}
		return artifacts[i].ScopeKey < artifacts[j].ScopeKey
	})
}

func materializeFocusedArtifacts(
	artifacts []agentArtifactRow,
) ([]agentcontract.FocusedArtifactContext, bool) {
	result := make([]agentcontract.FocusedArtifactContext, 0, min(len(artifacts), maxFocusedArtifacts))
	remaining := maxFocusedBytes
	truncated := len(artifacts) > maxFocusedArtifacts
	for _, artifact := range artifacts {
		if len(result) >= maxFocusedArtifacts {
			break
		}
		item := agentcontract.FocusedArtifactContext{
			ArtifactID: artifact.ArtifactID, ArtifactVersionID: artifact.ArtifactVersionID,
			RunID: artifact.RunID, CapabilityID: artifact.CapabilityID,
			ArtifactType: artifact.ArtifactType, ArtifactLabel: artifactDisplayLabel(artifact.ArtifactType, artifact.Payload),
			ScopeKey: artifact.ScopeKey, Status: artifact.Status, Version: artifact.Version,
		}
		if len(artifact.Payload) <= maxArtifactBytes && len(artifact.Payload) <= remaining {
			item.Payload = artifact.Payload
			remaining -= len(artifact.Payload)
		} else {
			limit := min(maxArtifactBytes, remaining)
			item.PayloadExcerpt = truncateUTF8(string(artifact.Payload), limit)
			item.ContentTruncated = true
			truncated = true
			remaining -= len(item.PayloadExcerpt)
		}
		result = append(result, item)
		if remaining <= 0 {
			truncated = truncated || len(result) < len(artifacts)
			break
		}
	}
	return result, truncated
}

func materializeArtifactReferences(
	artifacts []agentArtifactRow,
) ([]agentcontract.FocusedArtifactContext, bool) {
	result := make([]agentcontract.FocusedArtifactContext, 0, min(len(artifacts), maxFocusedArtifacts))
	for _, artifact := range artifacts {
		if len(result) >= maxFocusedArtifacts {
			break
		}
		result = append(result, agentcontract.FocusedArtifactContext{
			ArtifactID: artifact.ArtifactID, ArtifactVersionID: artifact.ArtifactVersionID,
			RunID: artifact.RunID, CapabilityID: artifact.CapabilityID,
			ArtifactType: artifact.ArtifactType, ArtifactLabel: artifactDisplayLabel(artifact.ArtifactType, artifact.Payload),
			ScopeKey: artifact.ScopeKey, Status: artifact.Status, Version: artifact.Version,
		})
	}
	return result, len(artifacts) > len(result)
}

func filterAgentArtifactsByDynamicLabel(
	artifacts []agentArtifactRow,
	content string,
) []agentArtifactRow {
	normalized := strings.ToLower(strings.TrimSpace(content))
	if normalized == "" {
		return nil
	}
	result := make([]agentArtifactRow, 0)
	for _, artifact := range artifacts {
		label := strings.ToLower(strings.TrimSpace(artifactDisplayLabel(artifact.ArtifactType, artifact.Payload)))
		if label == "" || label == strings.ToLower(artifactLabel(artifact.ArtifactType)) {
			continue
		}
		if strings.Contains(normalized, label) {
			result = append(result, artifact)
		}
	}
	return result
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func firstStringPointerValue(values ...*string) string {
	for _, value := range values {
		if value != nil && *value != "" {
			return *value
		}
	}
	return ""
}

func isDomainErrorCode(err error, code string) bool {
	var domain *DomainError
	return errors.As(err, &domain) && domain.Code == code
}
