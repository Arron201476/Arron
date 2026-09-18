package runtime

import "testing"

func TestValidateUploadSpecAcceptsVideoAt500MiB(t *testing.T) {
	err := validateUploadSpec(UploadItemSpec{
		ClientItemKey:     "video-500mb",
		Kind:              "video",
		OriginalFilename:  "video.mp4",
		DeclaredMIMEType:  "video/mp4",
		DeclaredSizeBytes: 500 << 20,
	})
	if err != nil {
		t.Fatalf("validateUploadSpec() error = %v", err)
	}
}

func TestValidateUploadSpecRejectsVideoAbove500MiB(t *testing.T) {
	err := validateUploadSpec(UploadItemSpec{
		ClientItemKey:     "video-over-500mb",
		Kind:              "video",
		OriginalFilename:  "video.mp4",
		DeclaredMIMEType:  "video/mp4",
		DeclaredSizeBytes: (500 << 20) + 1,
	})
	if err == nil {
		t.Fatal("validateUploadSpec() accepted a video above 500 MiB")
	}
}
