package capability

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxSkillResourceBytes = 10 << 20

type SkillResource struct {
	Path        string `json:"path"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentHash string `json:"content_hash"`
}

type SkillResourcePage struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset"`
	TotalChars int    `json:"total_chars"`
	Truncated  bool   `json:"truncated"`
	Kind       string `json:"kind,omitempty"`
	MediaType  string `json:"media_type,omitempty"`
	Filename   string `json:"filename,omitempty"`
	DataBase64 string `json:"data_base64,omitempty"`
}

func allowedSkillResource(name string) bool {
	if !fs.ValidPath(name) || strings.ContainsAny(name, "\\:") || path.Clean(name) != name {
		return false
	}
	return name == "SKILL.md" || strings.HasPrefix(name, "references/") ||
		strings.HasPrefix(name, "assets/") || strings.HasPrefix(name, "scripts/")
}

func snapshotSkillResources(directory string) ([]SkillResource, error) {
	resources := make([]SkillResource, 0)
	err := filepath.WalkDir(directory, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symbolic links are not allowed in Skill resources")
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(directory, file)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if !allowedSkillResource(name) {
			return nil
		}
		data, err := readSkillResourceBytes(directory, name)
		if err != nil {
			return err
		}
		if len(resources) >= 256 {
			return errors.New("Skill resources exceed 256 files")
		}
		hash := sha256.Sum256(data)
		resources = append(resources, SkillResource{Path: name, SizeBytes: int64(len(data)), ContentHash: "sha256:" + hex.EncodeToString(hash[:])})
		return nil
	})
	return resources, err
}

func readSkillResourceBytes(directory, name string) ([]byte, error) {
	if !allowedSkillResource(name) {
		return nil, errors.New("invalid Skill resource path")
	}
	// Check each directory component as well as the leaf, including Windows
	// junction resolution. Uploaded packages cannot introduce links.
	target := directory
	for _, part := range append([]string{""}, strings.Split(name, "/")...) {
		target = filepath.Join(target, part)
		info, err := os.Lstat(target)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("Skill resource path is unavailable or contains a link")
		}
	}
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || filepath.ToSlash(relative) != name {
		return nil, errors.New("Skill resource resolves outside its registered path")
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSkillResourceBytes {
		return nil, errors.New("Skill resource must be a regular file of at most 10 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSkillResourceBytes+1))
	if err != nil || len(data) > maxSkillResourceBytes {
		return nil, errors.New("cannot read bounded Skill resource")
	}
	return data, nil
}

func (s *SkillPackage) ReadResource(name string, offset, limit int) (SkillResourcePage, error) {
	if offset < 0 || limit < 1 || limit > 16000 {
		return SkillResourcePage{}, errors.New("offset must be nonnegative and limit must be 1..16000")
	}
	data, err := s.ReadResourceBytes(name)
	if err != nil {
		return SkillResourcePage{}, err
	}
	kind, mediaType, err := skillResourceMedia(name, data)
	if err != nil {
		return SkillResourcePage{}, err
	}
	if kind != "" {
		if offset != 0 {
			return SkillResourcePage{}, errors.New("binary Skill resources do not support text offsets")
		}
		return SkillResourcePage{Path: name, Kind: kind, MediaType: mediaType, Filename: path.Base(name),
			DataBase64: base64.StdEncoding.EncodeToString(data)}, nil
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return SkillResourcePage{}, errors.New("unsupported binary Skill resource; use UTF-8 text, PNG, JPEG, GIF, WebP, PDF or DOCX")
	}
	content := []rune(string(data))
	if offset > len(content) {
		return SkillResourcePage{}, errors.New("offset exceeds resource length")
	}
	end := min(offset+limit, len(content))
	return SkillResourcePage{Path: name, Content: string(content[offset:end]), Offset: offset,
		NextOffset: end, TotalChars: len(content), Truncated: end < len(content)}, nil
}

func (s *SkillPackage) ReadResourceBytes(name string) ([]byte, error) {
	var expected *SkillResource
	for index := range s.Resources {
		if s.Resources[index].Path == name {
			expected = &s.Resources[index]
			break
		}
	}
	if expected == nil {
		return nil, errors.New("resource is not part of this registered Skill version")
	}
	data, err := readSkillResourceBytes(s.Directory, name)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	if "sha256:"+hex.EncodeToString(hash[:]) != expected.ContentHash {
		return nil, errors.New("Skill resource changed after registration; reload the Skill version")
	}
	return data, nil
}

func skillResourceMedia(name string, data []byte) (string, string, error) {
	mediaType := http.DetectContentType(data)
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return "image", mediaType, nil
	case "application/pdf":
		return "file", mediaType, nil
	}
	if strings.EqualFold(path.Ext(name), ".docx") {
		reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil || len(reader.File) > 2048 {
			return "", "", errors.New("invalid DOCX Skill resource")
		}
		var contentTypes, document bool
		for _, file := range reader.File {
			contentTypes = contentTypes || file.Name == "[Content_Types].xml"
			document = document || file.Name == "word/document.xml"
		}
		if !contentTypes || !document {
			return "", "", errors.New("DOCX Skill resource is missing required entries")
		}
		return "file", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", nil
	}
	return "", "", nil
}
