package httpapi

import (
	"net/http"
	"strings"

	"content-agent/backend/internal/agentcontract"
	businessruntime "content-agent/backend/internal/runtime"
)

const messageRequestHashPrefix = "message-request-v2:"

func messageSubmissionMatches(request *http.Request, projectID string, body agentcontract.MessageRequest, hash string, prior businessruntime.AgentTurnSubmission) (bool, error) {
	if strings.HasPrefix(prior.RequestHash, messageRequestHashPrefix) {
		return prior.RequestHash == messageRequestHashPrefix+hash, nil
	}
	if prior.RequestHash == hash {
		return true, nil
	}
	// Legacy hashes describe the post-binding request, not the original bytes.
	// Reconstruct only that legacy automatic binding from the immutable receipt,
	// never today's assets or an edited queue entry. A new-format hash cannot use it.
	if body.CapabilityRef == nil || len(body.AttachmentRefs) != 0 || len(prior.Request.AttachmentRefs) > 1 {
		return false, nil
	}
	for _, attachment := range prior.Request.AttachmentRefs {
		if attachment.DisplayName != "" || attachment.Hidden || attachment.ContainerAssetID != nil {
			return false, nil
		}
	}
	body.AttachmentRefs = prior.Request.AttachmentRefs
	legacyHash, err := commandRequestHash(request, projectID, body)
	return err == nil && legacyHash == prior.RequestHash, err
}
