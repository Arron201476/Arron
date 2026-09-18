package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

type skillZIPTestEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func TestSkillArchiveRejectsImplicitDirectoryConflicts(t *testing.T) {
	for _, test := range []struct {
		name   string
		first  string
		second string
		code   string
	}{
		{"directory case", "assets/Logo/a.txt", "assets/logo/b.txt", "SKILL_PACKAGE_CASE_COLLISION"},
		{"ancestor case", "assets/Logo/deep/a.txt", "assets/logo/other/b.txt", "SKILL_PACKAGE_CASE_COLLISION"},
		{"file and directory", "assets/logo", "assets/logo/b.txt", "SKILL_PACKAGE_PATH_CONFLICT"},
		{"same directory", "assets/logo/a.txt", "assets/logo/b.txt", ""},
		{"separate directories", "assets/logo/a.txt", "assets/logos/b.txt", ""},
	} {
		for _, reverse := range []bool{false, true} {
			name := test.name
			first, second := test.first, test.second
			if reverse {
				name += " reversed"
				first, second = second, first
			}
			t.Run(name, func(t *testing.T) {
				archive := buildSkillZIP(t, []skillZIPTestEntry{
					{name: "SKILL.md", data: []byte("fixture")},
					{name: first, data: []byte("a")},
					{name: second, data: []byte("b")},
				})
				reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
				if err != nil {
					t.Fatal(err)
				}
				entries, _, err := inspectSkillArchive(reader.File)
				if test.code != "" {
					assertDomainCode(t, err, test.code)
				} else if err != nil || len(entries) != 3 {
					t.Fatalf("valid sibling entries rejected: %v, entries=%d", err, len(entries))
				}
			})
		}
	}
}

func TestSkillZIPInstallAcceptsRootlessAndWrappedPackages(t *testing.T) {
	root := t.TempDir()
	store, err := Open(filepath.Join(root, "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	for _, test := range []struct {
		name       string
		skillName  string
		capability string
		wrapper    bool
	}{
		{name: "rootless", skillName: "zip-rootless-skill", capability: "zip_rootless_skill"},
		{name: "wrapped", skillName: "zip-wrapped-skill", capability: "zip_wrapped_skill", wrapper: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := buildSkillZIP(t, validSkillZIPEntries(
				test.skillName, test.capability, "1.2.3", test.wrapper,
			))
			installed, err := store.InstallSkillZIP(
				context.Background(), test.name+".zip", bytes.NewReader(archive), "user_zip_test",
			)
			if err != nil {
				t.Fatalf("InstallSkillZIP() error = %v", err)
			}
			if installed.SkillName != test.skillName ||
				activeSkillVersionForTest(t, installed).Version != "1.2.3" {
				t.Fatalf("installed = %+v", installed)
			}
			workspaceRegistry, err := store.CapabilityRegistryForWorkspace(context.Background(), SharedWorkspaceID)
			if err != nil {
				t.Fatalf("CapabilityRegistryForWorkspace() error = %v", err)
			}
			assertRegistrySkillVersion(t, workspaceRegistry, test.capability, "1.2.3", true)
		})
	}
	assertDirectoryEmpty(t, filepath.Join(store.skillDataRoot, "quarantine"))
}

func TestSkillZIPSecurityPolicyRejectsMaliciousArchives(t *testing.T) {
	root := t.TempDir()
	store, err := Open(filepath.Join(root, "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	validSkill := []byte("---\nname: secure-test-skill\ndescription: Security policy fixture.\n---\n\nFollow safe instructions.\n")
	bomb := bytes.Repeat([]byte{0}, compressionRatioFloorSize*2)
	manyFiles := []skillZIPTestEntry{{name: "SKILL.md", data: validSkill}}
	for index := 0; index < maxSkillPackageFiles; index++ {
		manyFiles = append(manyFiles, skillZIPTestEntry{
			name: "references/item-" + leftPadInt(index, 3) + ".txt", data: []byte("x"),
		})
	}
	tests := []struct {
		name       string
		archive    []byte
		expected   string
		escapedRef string
	}{
		{
			name: "path traversal", expected: "SKILL_PACKAGE_PATH_UNSAFE", escapedRef: "escape.txt",
			archive: buildSkillZIP(t, []skillZIPTestEntry{
				{name: "SKILL.md", data: validSkill}, {name: "../escape.txt", data: []byte("escape")},
			}),
		},
		{
			name: "backslash traversal", expected: "SKILL_PACKAGE_PATH_UNSAFE",
			archive: buildSkillZIP(t, []skillZIPTestEntry{{name: "..\\SKILL.md", data: validSkill}}),
		},
		{
			name: "absolute path", expected: "SKILL_PACKAGE_PATH_UNSAFE",
			archive: buildSkillZIP(t, []skillZIPTestEntry{{name: "/SKILL.md", data: validSkill}}),
		},
		{
			name: "windows device path", expected: "SKILL_PACKAGE_PATH_UNSAFE",
			archive: buildSkillZIP(t, []skillZIPTestEntry{
				{name: "SKILL.md", data: validSkill}, {name: "references/CON.txt", data: []byte("x")},
			}),
		},
		{
			name: "duplicate path", expected: "SKILL_PACKAGE_DUPLICATE_PATH",
			archive: buildSkillZIP(t, []skillZIPTestEntry{
				{name: "SKILL.md", data: validSkill}, {name: "SKILL.md", data: validSkill},
			}),
		},
		{
			name: "case collision", expected: "SKILL_PACKAGE_CASE_COLLISION",
			archive: buildSkillZIP(t, []skillZIPTestEntry{
				{name: "SKILL.md", data: validSkill}, {name: "skill.md", data: validSkill},
			}),
		},
		{
			name: "symbolic link", expected: "SKILL_PACKAGE_SYMLINK_FORBIDDEN",
			archive: buildSkillZIP(t, []skillZIPTestEntry{
				{name: "SKILL.md", data: validSkill},
				{name: "references/link", data: []byte("target"), mode: os.ModeSymlink | 0o777},
			}),
		},
		{
			name: "script binary", expected: "SKILL_SCRIPT_FILE_UNSUPPORTED",
			archive: buildSkillZIP(t, []skillZIPTestEntry{
				{name: "SKILL.md", data: validSkill}, {name: "scripts/run.exe", data: []byte("binary")},
			}),
		},
		{
			name: "unsupported file", expected: "SKILL_PACKAGE_FILE_UNSUPPORTED",
			archive: buildSkillZIP(t, []skillZIPTestEntry{
				{name: "SKILL.md", data: validSkill}, {name: "README.md", data: []byte("x")},
			}),
		},
		{
			name: "multiple roots", expected: "SKILL_PACKAGE_MULTIPLE_ROOTS",
			archive: buildSkillZIP(t, []skillZIPTestEntry{
				{name: "secure-test-skill/SKILL.md", data: validSkill},
				{name: "outside/references/x.txt", data: []byte("x")},
			}),
		},
		{
			name: "compression bomb", expected: "SKILL_ARCHIVE_BOMB",
			archive: buildSkillZIP(t, []skillZIPTestEntry{
				{name: "SKILL.md", data: validSkill}, {name: "references/zeros.bin", data: bomb},
			}),
		},
		{
			name: "too many files", expected: "SKILL_PACKAGE_TOO_MANY_FILES",
			archive: buildSkillZIP(t, manyFiles),
		},
		{
			name: "invalid metadata", expected: "SKILL_PACKAGE_INVALID",
			archive: buildSkillZIP(t, []skillZIPTestEntry{{name: "SKILL.md", data: []byte("invalid")}}),
		},
		{name: "corrupt zip", expected: "SKILL_ARCHIVE_INVALID", archive: []byte("not a zip")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.InstallSkillZIP(
				context.Background(), "unsafe.zip", bytes.NewReader(test.archive), "user_security_test",
			)
			assertDomainCode(t, err, test.expected)
			assertDirectoryEmpty(t, filepath.Join(store.skillDataRoot, "quarantine"))
			if test.escapedRef != "" {
				if _, statErr := os.Stat(filepath.Join(root, test.escapedRef)); !os.IsNotExist(statErr) {
					t.Fatalf("escaped file exists: %v", statErr)
				}
			}
		})
	}
	installations, err := store.ListSkillInstallations(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(installations) != 0 {
		t.Fatalf("malicious archives polluted installations: %+v", installations)
	}
	attempts, err := store.ListSkillInstallAttempts(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != len(tests) {
		t.Fatalf("attempt count = %d, want %d", len(attempts), len(tests))
	}
	for _, attempt := range attempts {
		if attempt.Status != "failed" || attempt.FailureCode == "" || len(attempt.Diagnostics) != 1 {
			t.Fatalf("attempt = %+v", attempt)
		}
	}
}

func TestSkillArchiveDeclaredLimitsAndEncryption(t *testing.T) {
	tests := []struct {
		name     string
		header   zip.FileHeader
		expected string
	}{
		{
			name: "encrypted", expected: "SKILL_ARCHIVE_ENCRYPTED",
			header: zip.FileHeader{
				Name: "SKILL.md", Flags: 0x1, UncompressedSize64: 1, CompressedSize64: 1,
			},
		},
		{
			name: "single file too large", expected: "SKILL_PACKAGE_FILE_TOO_LARGE",
			header: zip.FileHeader{
				Name: "SKILL.md", UncompressedSize64: maxSkillFileBytes + 1, CompressedSize64: maxSkillFileBytes + 1,
			},
		},
		{
			name: "declared bomb", expected: "SKILL_ARCHIVE_BOMB",
			header: zip.FileHeader{
				Name: "SKILL.md", UncompressedSize64: compressionRatioFloorSize, CompressedSize64: 1,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.header.SetMode(0o644)
			_, _, err := inspectSkillArchive([]*zip.File{{FileHeader: test.header}})
			assertDomainCode(t, err, test.expected)
		})
	}
}

func TestSkillArchiveCompressedSizeAndSourceNameLimits(t *testing.T) {
	root := t.TempDir()
	quarantine := filepath.Join(root, "quarantine")
	if err := os.MkdirAll(quarantine, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := prepareSkillZIP(
		root, quarantine, "oversized.zip", io.LimitReader(zeroSkillReader{}, maxSkillArchiveBytes+1),
	)
	assertDomainCode(t, err, "SKILL_ARCHIVE_TOO_LARGE")

	longName := strings.Repeat("技能", 200) + ".zip"
	safe := safeSkillSourceName(longName, "fallback.zip")
	if len(safe) > 255 || !utf8.ValidString(safe) || !strings.HasSuffix(safe, ".zip") {
		t.Fatalf("safe source name is invalid: bytes=%d valid=%v", len(safe), utf8.ValidString(safe))
	}
	if got := safeSkillSourceName("C:\\unsafe\\folder\\name.zip", "fallback.zip"); got != "name.zip" {
		t.Fatalf("safe source name = %q", got)
	}
}

func validSkillZIPEntries(name, capabilityID, version string, wrapped bool) []skillZIPTestEntry {
	prefix := ""
	if wrapped {
		prefix = name + "/"
	}
	skillMarkdown := "---\nname: " + name + "\ndescription: ZIP installation fixture.\n---\n\nFollow the ZIP fixture instructions.\n"
	manifest := `{"schema_version":"1.0.0","id":"` + capabilityID +
		`","version":"` + version + `","execution_mode":"inline","ui":{}}`
	return []skillZIPTestEntry{
		{name: prefix + "SKILL.md", data: []byte(skillMarkdown)},
		{name: prefix + "content-agent/manifest.json", data: []byte(manifest)},
		{name: prefix + "references/guide.txt", data: []byte("reference")},
	}
}

func buildSkillZIP(t *testing.T, entries []skillZIPTestEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header.SetMode(mode)
		output, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("CreateHeader(%s) error = %v", entry.name, err)
		}
		if _, err := output.Write(entry.data); err != nil {
			t.Fatalf("write ZIP entry %s: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close ZIP: %v", err)
	}
	return buffer.Bytes()
}

func leftPadInt(value, width int) string {
	text := ""
	if value == 0 {
		text = "0"
	}
	for value > 0 {
		text = string(rune('0'+value%10)) + text
		value /= 10
	}
	for len(text) < width {
		text = "0" + text
	}
	return text
}

type zeroSkillReader struct{}

func (zeroSkillReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}
