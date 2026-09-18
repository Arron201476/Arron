package runtime

import "testing"

func TestValidateUploadSpecArchiveSizeBoundary(t *testing.T) {
	tests := []struct {
		name    string
		size    int64
		wantErr bool
	}{
		{name: "one gigabyte is accepted", size: 1 << 30},
		{name: "one byte over one gigabyte is rejected", size: (1 << 30) + 1, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateUploadSpec(UploadItemSpec{
				ClientItemKey:     "archive-boundary",
				Kind:              "archive",
				OriginalFilename:  "episodes.zip",
				DeclaredMIMEType:  "application/zip",
				DeclaredSizeBytes: test.size,
			})
			if (err != nil) != test.wantErr {
				t.Fatalf("validateUploadSpec() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
