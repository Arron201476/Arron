package httpapi

import (
	"reflect"
	"testing"

	businessruntime "content-agent/backend/internal/runtime"
)

func TestAcceptedRevisionHandoffUsesCompleteVersionSet(t *testing.T) {
	for _, test := range []struct {
		name                   string
		versions, scopes, want []string
		invalid                bool
	}{
		{name: "complete_set", versions: []string{"saved", "other"}, scopes: []string{"episode:1", "episode:2"}, want: []string{"saved", "other"}},
		{name: "legacy_single", scopes: []string{"episode:1"}, want: []string{"saved"}},
		{name: "legacy_single_no_scopes", want: []string{"saved"}},
		{name: "missing_multi_set", scopes: []string{"episode:1", "episode:2"}, invalid: true},
		{name: "missing_saved_version", versions: []string{"other"}, invalid: true},
		{name: "duplicate", versions: []string{"saved", "saved"}, invalid: true},
		{name: "empty_id", versions: []string{"saved", ""}, invalid: true},
		{name: "partial_set", versions: []string{"saved"}, scopes: []string{"episode:1", "episode:2"}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := businessruntime.VersionResult{ArtifactVersion: businessruntime.ArtifactVersion{ArtifactVersionID: "saved"}, PendingRefreshVersionIDs: test.versions, PendingRefreshScopes: test.scopes}
			versions, err := acceptedRevisionScriptVersions(result)
			if test.invalid {
				if err == nil {
					t.Fatal("incomplete version set accepted")
				}
				return
			}
			if err != nil || !reflect.DeepEqual(versions, test.want) {
				t.Fatalf("versions=%v error=%v", versions, err)
			}
			if len(result.PendingRefreshVersionIDs) > 0 {
				versions[0] = "changed"
				if result.PendingRefreshVersionIDs[0] != "saved" {
					t.Fatal("mutated the saved acceptance receipt")
				}
			}
		})
	}
}
