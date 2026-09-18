package runtime

import (
	"encoding/json"
)

type nonNovelContentClaim struct {
	ClaimID    string `json:"claim_id"`
	Status     string `json:"status"`
	Locked     bool   `json:"locked"`
	SourceRefs []struct {
		SourceType      string `json:"source_type"`
		AssetID         string `json:"asset_id"`
		AssetSnapshotID string `json:"asset_snapshot_id"`
		SourceUnitID    string `json:"source_unit_id"`
	} `json:"source_refs"`
}

func validateMaterialBankClaims(
	upstream []ContextUpstreamArtifact,
	payload json.RawMessage,
) error {
	var manifest sourceManifestPayload
	manifestFound := false
	for _, artifact := range upstream {
		if artifact.ArtifactType != "source_manifest" {
			continue
		}
		if err := json.Unmarshal(artifact.Content, &manifest); err != nil ||
			manifest.SourceKind != "non_novel" ||
			len(manifest.Units) == 0 {
			return domainError(
				"SOURCE_MANIFEST_INVALID",
				"非小说素材库引用的来源清单无效。",
			)
		}
		manifestFound = true
		break
	}
	if !manifestFound {
		return domainError(
			"DEPENDENCY_INCOMPLETE",
			"非小说素材库缺少来源清单。",
		)
	}

	var materialBank struct {
		SourceTrace struct {
			Claims []nonNovelContentClaim `json:"claims"`
		} `json:"source_trace"`
	}
	if err := json.Unmarshal(payload, &materialBank); err != nil ||
		len(materialBank.SourceTrace.Claims) == 0 {
		return domainError(
			"CONTENT_CLAIM_INVALID",
			"非小说素材库没有输出可追溯内容事实。",
		)
	}

	manifestUnits := make(map[string]sourceManifestUnit, len(manifest.Units))
	for _, unit := range manifest.Units {
		manifestUnits[unit.SourceUnitID] = unit
	}
	seenClaimIDs := make(map[string]struct{}, len(materialBank.SourceTrace.Claims))
	factCount := 0
	for _, claim := range materialBank.SourceTrace.Claims {
		if claim.ClaimID == "" {
			return domainError("CONTENT_CLAIM_INVALID", "内容事实缺少 claim_id。")
		}
		if _, exists := seenClaimIDs[claim.ClaimID]; exists {
			return domainError("CONTENT_CLAIM_INVALID", "内容事实 claim_id 重复。")
		}
		seenClaimIDs[claim.ClaimID] = struct{}{}

		if claim.Status == "CONFIRMED_CHANGE" {
			return domainError(
				"CONTENT_CLAIM_INVALID",
				"初始素材整理不能创建已确认变更。",
			)
		}
		if claim.Status != "FACT" {
			if claim.Locked {
				return domainError(
					"CONTENT_CLAIM_INVALID",
					"推断、提案和未知内容不能锁定为事实。",
				)
			}
			continue
		}
		factCount++
		if !claim.Locked {
			return domainError(
				"CONTENT_CLAIM_INVALID",
				"用户明确提供的事实必须锁定。",
			)
		}
		matched := false
		for _, reference := range claim.SourceRefs {
			unit, exists := manifestUnits[reference.SourceUnitID]
			if exists &&
				reference.SourceType == "asset_text_range" &&
				reference.AssetID == unit.AssetID &&
				reference.AssetSnapshotID == unit.AssetSnapshotID {
				matched = true
				break
			}
		}
		if !matched {
			return domainError(
				"CONTENT_CLAIM_INVALID",
				"用户事实没有精确引用当前来源清单。",
			)
		}
	}
	if factCount == 0 {
		return domainError(
			"CONTENT_CLAIM_INVALID",
			"非小说素材库至少需要一条用户明确提供的事实。",
		)
	}
	return nil
}
