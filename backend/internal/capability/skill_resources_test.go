package capability

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestSkillResourcesReturnNativeMediaWithoutTextPagination(t *testing.T) {
	root := t.TempDir()
	directory := writeTestSkill(t, root, "media-skill", "Read resources.", "1.0.0")
	if err := os.MkdirAll(filepath.Join(directory, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	var pngData, docxData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(&docxData)
	for _, name := range []string{"[Content_Types].xml", "word/document.xml"} {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("<fixture/>")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	fixtures := map[string][]byte{"picture.png": pngData.Bytes(), "guide.pdf": []byte("%PDF-1.4\n%%EOF"), "guide.docx": docxData.Bytes(), "invalid.docx": []byte("not a document")}
	for name, data := range fixtures {
		if err := os.WriteFile(filepath.Join(directory, "assets", name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	skill, err := InspectSkillPackage(root, SkillRoot{Scope: SkillScopeWorkspace, Path: root}, directory)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range fixtures {
		page, err := skill.ReadResource("assets/"+name, 0, 10)
		if name == "invalid.docx" {
			if err == nil {
				t.Fatal("invalid DOCX accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.StdEncoding.DecodeString(page.DataBase64)
		if err != nil || !bytes.Equal(data, decoded) || page.Kind == "" || page.MediaType == "" || page.Filename != name || page.Truncated || page.Content != "" {
			t.Fatalf("incorrect media resource: %+v, %v", page, err)
		}
		if _, err := skill.ReadResource("assets/"+name, 1, 10); err == nil {
			t.Fatal("binary text offset accepted")
		}
		if err := os.WriteFile(filepath.Join(directory, "assets", name), []byte("changed"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := skill.ReadResource("assets/"+name, 0, 10); err == nil {
			t.Fatal("changed binary resource accepted")
		}
	}
}

func TestSkillResourcesAreVersionedBoundedAndPaged(t *testing.T) {
	root := t.TempDir()
	directory := writeTestSkill(t, root, "resource-skill", "Read resources.", "1.0.0")
	if err := os.MkdirAll(filepath.Join(directory, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "references", "guide.txt")
	if err := os.WriteFile(file, []byte("甲乙丙丁戊"), 0o644); err != nil {
		t.Fatal(err)
	}
	skill, err := InspectSkillPackage(root, SkillRoot{Scope: SkillScopeWorkspace, Path: root}, directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(skill.Resources) != 2 {
		t.Fatalf("resources = %+v", skill.Resources)
	}
	page, err := skill.ReadResource("references/guide.txt", 1, 2)
	if err != nil || page.Content != "乙丙" || page.NextOffset != 3 || !page.Truncated || page.TotalChars != 5 {
		t.Fatalf("page = %+v, error = %v", page, err)
	}
	last, err := skill.ReadResource("references/guide.txt", page.NextOffset, 16000)
	if err != nil || last.Content != "丁戊" || last.Truncated {
		t.Fatalf("last page = %+v, error = %v", last, err)
	}
	for _, name := range []string{"../SKILL.md", "references/../SKILL.md", "references\\guide.txt", "references/guide.txt:secret", "content-agent/manifest.json", "/SKILL.md"} {
		if _, err := skill.ReadResource(name, 0, 20); err == nil {
			t.Errorf("unsafe resource path accepted: %s", name)
		}
	}
	for _, limits := range [][2]int{{-1, 20}, {0, 0}, {0, 16001}, {6, 20}} {
		if _, err := skill.ReadResource("references/guide.txt", limits[0], limits[1]); err == nil {
			t.Errorf("invalid range accepted: %v", limits)
		}
	}
	if err := os.WriteFile(file, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.ReadResource("references/guide.txt", 0, 20); err == nil {
		t.Fatal("resource changed after version registration was accepted")
	}
}

func TestSkillResourcesRejectBinaryAndLinkedFiles(t *testing.T) {
	root := t.TempDir()
	directory := writeTestSkill(t, root, "binary-skill", "Read resources.", "1.0.0")
	assets := filepath.Join(directory, "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(assets, "image.bin")
	if err := os.WriteFile(file, []byte{0, 255, 128}, 0o644); err != nil {
		t.Fatal(err)
	}
	skill, err := InspectSkillPackage(root, SkillRoot{Scope: SkillScopeWorkspace, Path: root}, directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := skill.ReadResource("assets/image.bin", 0, 20); err == nil {
		t.Fatal("binary resource returned as text")
	}
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, file); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := skill.ReadResource("assets/image.bin", 0, 20); err == nil {
		t.Fatal("symlink replacement was accepted")
	}
}
