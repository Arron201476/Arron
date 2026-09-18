package runtime

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"content-agent/backend/internal/capability"
)

const (
	maxSkillArchiveBytes      = 20 << 20
	maxSkillExpandedBytes     = 50 << 20
	maxSkillFileBytes         = 10 << 20
	maxSkillPackageFiles      = 256
	maxSkillCompressionRatio  = 100
	compressionRatioFloorSize = 64 << 10
)

type skillArchiveEntry struct {
	file         *zip.File
	archivePath  string
	packagePath  string
	uncompressed int64
	compressed   int64
}

func prepareSkillZIP(
	projectRoot string,
	quarantineRoot string,
	sourceName string,
	source io.Reader,
) (*capability.SkillPackage, error) {
	if strings.ToLower(filepath.Ext(sourceName)) != ".zip" {
		return nil, domainError("SKILL_ARCHIVE_REQUIRED", "Skill 上传文件必须是 ZIP。")
	}
	archivePath := filepath.Join(quarantineRoot, "upload.zip")
	archive, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create Skill quarantine archive: %w", err)
	}
	written, copyErr := io.Copy(archive, io.LimitReader(source, maxSkillArchiveBytes+1))
	closeErr := archive.Close()
	if copyErr != nil || closeErr != nil {
		return nil, fmt.Errorf("write Skill quarantine archive: %w", errors.Join(copyErr, closeErr))
	}
	if written > maxSkillArchiveBytes {
		return nil, domainError("SKILL_ARCHIVE_TOO_LARGE", "Skill ZIP 超过 20 MiB 限制。")
	}

	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, domainError("SKILL_ARCHIVE_INVALID", "Skill ZIP 损坏或无法读取。")
	}
	defer reader.Close()
	entries, wrapper, err := inspectSkillArchive(reader.File)
	if err != nil {
		return nil, err
	}
	payloadRoot := filepath.Join(quarantineRoot, "payload")
	if err := os.Mkdir(payloadRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create Skill quarantine payload: %w", err)
	}
	for _, entry := range entries {
		target := filepath.Join(payloadRoot, filepath.FromSlash(entry.packagePath))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		input, err := entry.file.Open()
		if err != nil {
			return nil, domainError("SKILL_ARCHIVE_INVALID", "Skill ZIP 中有文件无法读取。")
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			input.Close()
			return nil, err
		}
		copied, copyErr := io.Copy(output, io.LimitReader(input, maxSkillFileBytes+1))
		inputCloseErr := input.Close()
		outputCloseErr := output.Close()
		if copyErr != nil || inputCloseErr != nil || outputCloseErr != nil || copied != entry.uncompressed {
			return nil, domainError("SKILL_ARCHIVE_INVALID", "Skill ZIP 解压结果与目录记录不一致。")
		}
	}
	return finalizeQuarantinedSkill(projectRoot, quarantineRoot, payloadRoot, wrapper)
}

func prepareSkillDirectory(
	projectRoot string,
	quarantineRoot string,
	sourceDirectory string,
) (*capability.SkillPackage, error) {
	sourceDirectory, err := filepath.Abs(sourceDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolve Skill source directory: %w", err)
	}
	info, err := os.Lstat(sourceDirectory)
	if err != nil {
		return nil, fmt.Errorf("inspect Skill source directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, domainError("SKILL_PACKAGE_PATH_UNSAFE", "Skill 来源必须是普通目录，不能是符号链接。")
	}

	payloadRoot := filepath.Join(quarantineRoot, "payload")
	if err := os.Mkdir(payloadRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create Skill quarantine payload: %w", err)
	}
	seen := make(map[string]string)
	fileCount := 0
	var total int64
	err = filepath.WalkDir(sourceDirectory, func(sourcePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if sourcePath == sourceDirectory {
			return nil
		}
		relative, err := filepath.Rel(sourceDirectory, sourcePath)
		if err != nil {
			return err
		}
		packagePath, err := normalizeSkillPackagePath(filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return domainError("SKILL_PACKAGE_SYMLINK_FORBIDDEN", "Skill 包不能包含符号链接。")
		}
		if entry.IsDir() {
			return nil
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if !entryInfo.Mode().IsRegular() {
			return domainError("SKILL_PACKAGE_ENTRY_UNSAFE", "Skill 包只能包含普通文件。")
		}
		if err := validateSkillPackageFile(packagePath); err != nil {
			return err
		}
		if err := registerSkillPackagePath(seen, packagePath); err != nil {
			return err
		}
		fileCount++
		if fileCount > maxSkillPackageFiles {
			return domainError("SKILL_PACKAGE_TOO_MANY_FILES", "Skill 包文件数量超过 256 个。")
		}
		if entryInfo.Size() > maxSkillFileBytes {
			return domainError("SKILL_PACKAGE_FILE_TOO_LARGE", "Skill 包中有文件超过 10 MiB。")
		}
		total += entryInfo.Size()
		if total > maxSkillExpandedBytes {
			return domainError("SKILL_PACKAGE_EXPANDED_TOO_LARGE", "Skill 包总大小超过 50 MiB。")
		}
		target := filepath.Join(payloadRoot, filepath.FromSlash(packagePath))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyRegularFile(sourcePath, target, entryInfo.Size())
	})
	if err != nil {
		return nil, err
	}
	return finalizeQuarantinedSkill(
		projectRoot,
		quarantineRoot,
		payloadRoot,
		filepath.Base(sourceDirectory),
	)
}

func inspectSkillArchive(files []*zip.File) ([]skillArchiveEntry, string, error) {
	type inspectedEntry struct {
		file         *zip.File
		archivePath  string
		uncompressed int64
		compressed   int64
	}
	inspected := make([]inspectedEntry, 0, len(files))
	seenArchivePaths := make(map[string]string)
	skillFiles := make([]string, 0, 1)
	var totalExpanded, totalCompressed int64
	for _, file := range files {
		archivePath, err := normalizeSkillPackagePath(file.Name)
		if err != nil {
			return nil, "", err
		}
		if file.FileInfo().IsDir() {
			continue
		}
		if file.Flags&0x1 != 0 {
			return nil, "", domainError("SKILL_ARCHIVE_ENCRYPTED", "Skill ZIP 不能包含加密文件。")
		}
		if file.Mode()&os.ModeSymlink != 0 {
			return nil, "", domainError("SKILL_PACKAGE_SYMLINK_FORBIDDEN", "Skill 包不能包含符号链接。")
		}
		if !file.Mode().IsRegular() {
			return nil, "", domainError("SKILL_PACKAGE_ENTRY_UNSAFE", "Skill 包只能包含普通文件。")
		}
		if err := registerSkillPackagePath(seenArchivePaths, archivePath); err != nil {
			return nil, "", err
		}
		uncompressed := int64(file.UncompressedSize64)
		compressed := int64(file.CompressedSize64)
		if uncompressed > maxSkillFileBytes {
			return nil, "", domainError("SKILL_PACKAGE_FILE_TOO_LARGE", "Skill 包中有文件超过 10 MiB。")
		}
		if uncompressed >= compressionRatioFloorSize &&
			(compressed == 0 || uncompressed/compressed > maxSkillCompressionRatio) {
			return nil, "", domainError("SKILL_ARCHIVE_BOMB", "Skill ZIP 中有文件压缩比异常。")
		}
		totalExpanded += uncompressed
		totalCompressed += compressed
		if totalExpanded > maxSkillExpandedBytes {
			return nil, "", domainError("SKILL_PACKAGE_EXPANDED_TOO_LARGE", "Skill 包解压后超过 50 MiB。")
		}
		if len(inspected) >= maxSkillPackageFiles {
			return nil, "", domainError("SKILL_PACKAGE_TOO_MANY_FILES", "Skill 包文件数量超过 256 个。")
		}
		if archivePath == "SKILL.md" || strings.HasSuffix(archivePath, "/SKILL.md") {
			skillFiles = append(skillFiles, archivePath)
		}
		inspected = append(inspected, inspectedEntry{
			file: file, archivePath: archivePath,
			uncompressed: uncompressed, compressed: compressed,
		})
	}
	if totalExpanded >= compressionRatioFloorSize &&
		(totalCompressed == 0 || totalExpanded/totalCompressed > maxSkillCompressionRatio) {
		return nil, "", domainError("SKILL_ARCHIVE_BOMB", "Skill ZIP 总压缩比异常。")
	}
	if len(skillFiles) != 1 {
		return nil, "", domainError("SKILL_PACKAGE_INVALID", "Skill ZIP 必须且只能包含一个 SKILL.md。")
	}
	skillFile := skillFiles[0]
	wrapper := strings.TrimSuffix(skillFile, "SKILL.md")
	wrapper = strings.TrimSuffix(wrapper, "/")
	entries := make([]skillArchiveEntry, 0, len(inspected))
	seenPackagePaths := make(map[string]string)
	for _, entry := range inspected {
		packagePath := entry.archivePath
		if wrapper != "" {
			prefix := wrapper + "/"
			if !strings.HasPrefix(packagePath, prefix) {
				return nil, "", domainError("SKILL_PACKAGE_MULTIPLE_ROOTS", "Skill ZIP 在包目录外包含文件。")
			}
			packagePath = strings.TrimPrefix(packagePath, prefix)
		}
		if err := validateSkillPackageFile(packagePath); err != nil {
			return nil, "", err
		}
		if err := registerSkillPackagePath(seenPackagePaths, packagePath); err != nil {
			return nil, "", err
		}
		entries = append(entries, skillArchiveEntry{
			file: entry.file, archivePath: entry.archivePath, packagePath: packagePath,
			uncompressed: entry.uncompressed, compressed: entry.compressed,
		})
	}
	return entries, path.Base(wrapper), nil
}

func finalizeQuarantinedSkill(
	projectRoot string,
	quarantineRoot string,
	payloadRoot string,
	wrapperName string,
) (*capability.SkillPackage, error) {
	skillBytes, err := os.ReadFile(filepath.Join(payloadRoot, "SKILL.md"))
	if err != nil {
		return nil, domainError("SKILL_PACKAGE_INVALID", "Skill 包缺少根目录 SKILL.md。")
	}
	document, err := capability.ParseSkillDocument(skillBytes)
	if err != nil {
		return nil, domainError("SKILL_PACKAGE_INVALID", "SKILL.md 元数据或正文无效。")
	}
	if wrapperName != "" && wrapperName != "." && wrapperName != document.Name {
		return nil, domainError("SKILL_PACKAGE_NAME_MISMATCH", "ZIP 根目录名称必须与 Skill name 一致。")
	}
	packageDirectory := filepath.Join(quarantineRoot, document.Name)
	if err := renameManagedSkillPath(payloadRoot, packageDirectory); err != nil {
		return nil, fmt.Errorf("name quarantined Skill package: %w", err)
	}
	skill, err := capability.InspectSkillPackage(
		projectRoot,
		capability.SkillRoot{
			Scope: capability.SkillScopeWorkspace, Path: quarantineRoot, Priority: 250,
		},
		packageDirectory,
	)
	if err != nil {
		return nil, domainError("SKILL_PACKAGE_INVALID", "Skill 包未通过标准元数据校验。")
	}
	return skill, nil
}

func normalizeSkillPackagePath(value string) (string, error) {
	value = strings.ReplaceAll(value, "\\", "/")
	if value == "" || strings.ContainsRune(value, 0) || strings.HasPrefix(value, "/") {
		return "", domainError("SKILL_PACKAGE_PATH_UNSAFE", "Skill 包包含不安全路径。")
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", domainError("SKILL_PACKAGE_PATH_UNSAFE", "Skill 包包含路径逃逸。")
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == "" || segment == "." || segment == ".." ||
			strings.TrimSpace(segment) != segment || strings.Contains(segment, ":") ||
			strings.HasSuffix(segment, ".") || windowsReservedPathSegment(segment) {
			return "", domainError("SKILL_PACKAGE_PATH_UNSAFE", "Skill 包包含不兼容的文件路径。")
		}
	}
	if filepath.IsAbs(filepath.FromSlash(cleaned)) || filepath.VolumeName(filepath.FromSlash(cleaned)) != "" {
		return "", domainError("SKILL_PACKAGE_PATH_UNSAFE", "Skill 包包含绝对路径。")
	}
	return cleaned, nil
}

func windowsReservedPathSegment(segment string) bool {
	base := strings.ToUpper(strings.TrimSuffix(segment, filepath.Ext(segment)))
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return true
	}
	for _, prefix := range []string{"COM", "LPT"} {
		if len(base) == 4 && strings.HasPrefix(base, prefix) && base[3] >= '1' && base[3] <= '9' {
			return true
		}
	}
	return false
}

func registerSkillPackagePath(seen map[string]string, packagePath string) error {
	key := strings.ToLower(packagePath)
	if previous, exists := seen[key]; exists {
		if previous == packagePath {
			return domainError("SKILL_PACKAGE_DUPLICATE_PATH", "Skill 包包含重复文件路径。")
		}
		return domainError("SKILL_PACKAGE_CASE_COLLISION", "Skill 包包含仅大小写不同的冲突路径。")
	}
	if _, exists := seen[key+"/"]; exists {
		return domainError("SKILL_PACKAGE_PATH_CONFLICT", "Skill 包中的文件与目录路径冲突。")
	}
	// Trailing slashes distinguish implicit directories from regular files.
	parents := make([]string, 0)
	for parent := path.Dir(packagePath); parent != "."; parent = path.Dir(parent) {
		parentKey := strings.ToLower(parent)
		if _, exists := seen[parentKey]; exists {
			return domainError("SKILL_PACKAGE_PATH_CONFLICT", "Skill 包中的文件与目录路径冲突。")
		}
		if previous, exists := seen[parentKey+"/"]; exists && previous != parent {
			return domainError("SKILL_PACKAGE_CASE_COLLISION", "Skill 包包含仅大小写不同的冲突路径。")
		}
		parents = append(parents, parent)
	}
	for _, parent := range parents {
		seen[strings.ToLower(parent)+"/"] = parent
	}
	seen[key] = packagePath
	return nil
}

func validateSkillPackageFile(packagePath string) error {
	if packagePath == "SKILL.md" || packagePath == "agents/openai.yaml" ||
		packagePath == "content-agent/manifest.json" ||
		packagePath == "content-agent/workflow.json" {
		return nil
	}
	first, _, _ := strings.Cut(packagePath, "/")
	if first == "scripts" {
		extension := strings.ToLower(path.Ext(packagePath))
		if extension == ".py" || extension == ".json" || extension == ".toml" ||
			extension == ".yaml" || extension == ".yml" || extension == ".txt" {
			return nil
		}
		return domainError("SKILL_SCRIPT_FILE_UNSUPPORTED", "Skill 脚本目录只能包含 Python 源码和声明式数据文件。")
	}
	if first == "references" || first == "assets" || first == "schemas" || first == "prompts" {
		return nil
	}
	return domainError("SKILL_PACKAGE_FILE_UNSUPPORTED", "Skill 包包含当前策略不允许的文件。")
}

func copyRegularFile(sourcePath, targetPath string, expectedSize int64) error {
	input, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	output, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		input.Close()
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, maxSkillFileBytes+1))
	inputCloseErr := input.Close()
	outputCloseErr := output.Close()
	if copyErr != nil || inputCloseErr != nil || outputCloseErr != nil {
		return errors.Join(copyErr, inputCloseErr, outputCloseErr)
	}
	if written != expectedSize {
		return domainError("SKILL_PACKAGE_CHANGED", "Skill 目录在导入过程中发生变化。")
	}
	return nil
}
