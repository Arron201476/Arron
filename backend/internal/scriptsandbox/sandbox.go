package scriptsandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultSummaryBytes    = 16 * 1024
	maxArtifactFiles       = 128
	maxInputBytes          = 256 * 1024
	engineOperationTimeout = 30 * time.Second
)

type Limits struct {
	TimeoutSeconds int     `json:"timeout_seconds"`
	CPUCount       float64 `json:"cpu_count"`
	MemoryBytes    int64   `json:"memory_bytes"`
	ProcessCount   int     `json:"process_count"`
	DiskBytes      int64   `json:"disk_bytes"`
	TempBytes      int64   `json:"temp_bytes"`
}

func DefaultLimits() Limits {
	return Limits{
		TimeoutSeconds: 30,
		CPUCount:       0.5,
		MemoryBytes:    256 << 20,
		ProcessCount:   32,
		DiskBytes:      16 << 20,
		TempBytes:      16 << 20,
	}
}

func (limits Limits) Validate() error {
	if limits.TimeoutSeconds < 1 || limits.TimeoutSeconds > 300 {
		return errors.New("timeout_seconds must be between 1 and 300")
	}
	if math.IsNaN(limits.CPUCount) || math.IsInf(limits.CPUCount, 0) || limits.CPUCount < 0.1 || limits.CPUCount > 4 {
		return errors.New("cpu_count must be between 0.1 and 4")
	}
	if limits.MemoryBytes < 32<<20 || limits.MemoryBytes > 2<<30 {
		return errors.New("memory_bytes must be between 32 MiB and 2 GiB")
	}
	if limits.ProcessCount < 4 || limits.ProcessCount > 256 {
		return errors.New("process_count must be between 4 and 256")
	}
	if limits.DiskBytes < 1<<20 || limits.DiskBytes > 256<<20 {
		return errors.New("disk_bytes must be between 1 MiB and 256 MiB")
	}
	if limits.TempBytes < 1<<20 || limits.TempBytes > 256<<20 {
		return errors.New("temp_bytes must be between 1 MiB and 256 MiB")
	}
	return nil
}

type Status struct {
	Available   bool     `json:"available"`
	Adapter     string   `json:"adapter"`
	Engine      string   `json:"engine,omitempty"`
	Runtimes    []string `json:"runtimes"`
	ReasonCode  string   `json:"reason_code,omitempty"`
	UserMessage string   `json:"user_message,omitempty"`
}

type Request struct {
	ExecutionID string
	SkillRoot   string
	ScriptPath  string
	Runtime     string
	Input       json.RawMessage
	WorkRoot    string
	OutputRoot  string
	Limits      Limits
}

type Artifact struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type Result struct {
	Adapter       string     `json:"adapter"`
	Engine        string     `json:"engine"`
	Runtime       string     `json:"runtime"`
	Image         string     `json:"image"`
	ExitCode      int        `json:"exit_code"`
	StdoutSummary string     `json:"stdout_summary"`
	StderrSummary string     `json:"stderr_summary"`
	Artifacts     []Artifact `json:"artifacts"`
	StartedAt     time.Time  `json:"started_at"`
	CompletedAt   time.Time  `json:"completed_at"`
}

type Sandbox interface {
	Status() Status
	Execute(context.Context, Request) (Result, error)
}

type Error struct {
	Code    string
	Message string
}

func (err *Error) Error() string { return err.Code + ": " + err.Message }

func ErrorCode(err error) string {
	var sandboxError *Error
	if errors.As(err, &sandboxError) {
		return sandboxError.Code
	}
	return "SKILL_SCRIPT_EXECUTION_FAILED"
}

type Disabled struct {
	HostAdapter string
	ReasonCode  string
	Message     string
}

func (disabled Disabled) Status() Status {
	reason := disabled.ReasonCode
	if reason == "" {
		reason = "SCRIPT_SANDBOX_NOT_CONFIGURED"
	}
	message := disabled.Message
	if message == "" {
		message = "脚本沙箱尚未配置；平台不会回退到宿主机执行。"
	}
	return Status{
		Available: false, Adapter: disabled.HostAdapter,
		Runtimes: []string{}, ReasonCode: reason, UserMessage: message,
	}
}

func (disabled Disabled) Execute(context.Context, Request) (Result, error) {
	status := disabled.Status()
	return Result{}, &Error{Code: status.ReasonCode, Message: status.UserMessage}
}

type Config struct {
	EnginePath  string
	Adapter     string
	PythonImage string
}

func NewFromEnvironment() (Sandbox, error) {
	adapter := runtime.GOOS
	if configured := strings.TrimSpace(os.Getenv("CONTENT_AGENT_SCRIPT_SANDBOX_ADAPTER")); configured != "" {
		adapter = configured
	}
	engine := strings.TrimSpace(os.Getenv("CONTENT_AGENT_SCRIPT_OCI_COMMAND"))
	image := strings.TrimSpace(os.Getenv("CONTENT_AGENT_SCRIPT_PYTHON_IMAGE"))
	if engine == "" && image == "" {
		return Disabled{HostAdapter: adapter}, nil
	}
	if engine == "" || image == "" {
		return nil, errors.New("CONTENT_AGENT_SCRIPT_OCI_COMMAND and CONTENT_AGENT_SCRIPT_PYTHON_IMAGE must be configured together")
	}
	absolute, err := filepath.Abs(engine)
	if err != nil || !filepath.IsAbs(engine) {
		return nil, errors.New("CONTENT_AGENT_SCRIPT_OCI_COMMAND must be an absolute path")
	}
	info, err := os.Stat(absolute)
	if err != nil || info.IsDir() {
		return nil, errors.New("CONTENT_AGENT_SCRIPT_OCI_COMMAND must reference an existing executable")
	}
	return NewOCI(Config{EnginePath: absolute, Adapter: adapter, PythonImage: image})
}

type commandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type commandRunner interface {
	Run(context.Context, string, []string, int64, int64) (commandResult, error)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(
	ctx context.Context,
	name string,
	args []string,
	stdoutLimit int64,
	stderrLimit int64,
) (commandResult, error) {
	return execCommandRunner{}.RunInput(ctx, name, args, nil, stdoutLimit, stderrLimit)
}

func (execCommandRunner) RunInput(
	ctx context.Context,
	name string,
	args []string,
	input io.Reader,
	stdoutLimit int64,
	stderrLimit int64,
) (commandResult, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = minimalEngineEnvironment()
	command.Stdin = input
	stdout := &cappedBuffer{limit: stdoutLimit}
	stderr := &cappedBuffer{limit: stderrLimit}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	result := commandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: 0}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
	} else if err != nil {
		result.ExitCode = -1
	}
	if stdout.exceeded || stderr.exceeded {
		return result, &Error{Code: "SKILL_SCRIPT_OUTPUT_LIMIT_EXCEEDED", Message: "脚本进程输出超过平台限制。"}
	}
	return result, err
}

func minimalEngineEnvironment() []string {
	keys := []string{"SYSTEMROOT", "WINDIR", "TEMP", "TMP", "TMPDIR", "XDG_RUNTIME_DIR"}
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			environment = append(environment, key+"="+value)
		}
	}
	return environment
}

type cappedBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	exceeded bool
}

func (buffer *cappedBuffer) Write(data []byte) (int, error) {
	remaining := buffer.limit - int64(buffer.buffer.Len())
	if remaining <= 0 {
		buffer.exceeded = true
		return len(data), nil
	}
	write := data
	if int64(len(write)) > remaining {
		write = write[:remaining]
		buffer.exceeded = true
	}
	_, _ = buffer.buffer.Write(write)
	return len(data), nil
}

func (buffer *cappedBuffer) Bytes() []byte { return bytes.Clone(buffer.buffer.Bytes()) }

type hostAdapter interface {
	Name() string
	BindSource(string) (string, error)
}

type windowsAdapter struct{}

func (windowsAdapter) Name() string { return "windows" }
func (windowsAdapter) BindSource(value string) (string, error) {
	return safeAbsoluteBindPath(value)
}

type linuxAdapter struct{}

func (linuxAdapter) Name() string { return "linux" }
func (linuxAdapter) BindSource(value string) (string, error) {
	return safeAbsoluteBindPath(value)
}

func safeAbsoluteBindPath(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil || strings.ContainsAny(absolute, ",\r\n") {
		return "", errors.New("sandbox bind source is invalid")
	}
	return filepath.Clean(absolute), nil
}

type OCI struct {
	enginePath     string
	engineName     string
	adapter        hostAdapter
	pythonImage    string
	runner         commandRunner
	workspaceLocks workspaceOperationLocks
	workspacePTYs  workspacePTYRegistry
}

func NewOCI(config Config) (*OCI, error) {
	enginePath := strings.TrimSpace(config.EnginePath)
	if !filepath.IsAbs(enginePath) {
		return nil, errors.New("OCI engine path must be absolute")
	}
	info, err := os.Stat(enginePath)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("OCI engine path must reference an existing regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("OCI engine path is not executable")
	}
	return newOCI(config, execCommandRunner{})
}

func newOCI(config Config, runner commandRunner) (*OCI, error) {
	if runner == nil {
		return nil, errors.New("command runner is required")
	}
	enginePath := filepath.Clean(strings.TrimSpace(config.EnginePath))
	if enginePath == "" {
		return nil, errors.New("OCI engine path is required")
	}
	engineName := strings.TrimSuffix(strings.ToLower(filepath.Base(enginePath)), ".exe")
	if engineName != "docker" && engineName != "podman" {
		return nil, errors.New("OCI engine must be docker or podman")
	}
	image := strings.TrimSpace(config.PythonImage)
	if !pinnedImagePattern.MatchString(image) {
		return nil, errors.New("python image must be pinned by sha256 digest")
	}
	var adapter hostAdapter
	switch strings.ToLower(strings.TrimSpace(config.Adapter)) {
	case "windows":
		adapter = windowsAdapter{}
	case "linux":
		adapter = linuxAdapter{}
	default:
		return nil, errors.New("script sandbox adapter must be windows or linux")
	}
	return &OCI{
		enginePath: enginePath, engineName: engineName, adapter: adapter,
		pythonImage: image, runner: runner,
	}, nil
}

var (
	pinnedImagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*@sha256:[a-f0-9]{64}$`)
	executionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,95}$`)
)

func (sandbox *OCI) Status() Status {
	return Status{
		Available: true, Adapter: sandbox.adapter.Name(), Engine: sandbox.engineName,
		Runtimes: []string{"python"},
	}
}

func (sandbox *OCI) PythonImage() string { return sandbox.pythonImage }

func (sandbox *OCI) Execute(ctx context.Context, request Request) (result Result, returnedErr error) {
	startedAt := time.Now().UTC()
	result = Result{
		Adapter: sandbox.adapter.Name(), Engine: sandbox.engineName,
		Runtime: request.Runtime, Image: sandbox.pythonImage, ExitCode: -1,
		Artifacts: []Artifact{}, StartedAt: startedAt,
	}
	if !executionIDPattern.MatchString(request.ExecutionID) {
		return result, &Error{Code: "SKILL_SCRIPT_REQUEST_INVALID", Message: "脚本执行 ID 无效。"}
	}
	if request.Runtime != "python" {
		return result, &Error{Code: "SKILL_SCRIPT_RUNTIME_UNSUPPORTED", Message: "脚本 runtime 未被平台允许。"}
	}
	if err := request.Limits.Validate(); err != nil {
		return result, &Error{Code: "SKILL_SCRIPT_LIMITS_INVALID", Message: err.Error()}
	}
	if len(request.Input) == 0 || len(request.Input) > maxInputBytes || !json.Valid(request.Input) {
		return result, &Error{Code: "SKILL_SCRIPT_INPUT_INVALID", Message: "脚本输入必须是合法且不超过 256 KiB 的 JSON。"}
	}
	scriptPath, err := validateScriptPath(request.SkillRoot, request.ScriptPath)
	if err != nil {
		return result, &Error{Code: "SKILL_SCRIPT_PATH_UNSAFE", Message: err.Error()}
	}
	workRoot, err := sandbox.adapter.BindSource(request.WorkRoot)
	if err != nil {
		return result, &Error{Code: "SKILL_SCRIPT_WORKSPACE_UNSAFE", Message: err.Error()}
	}
	skillRoot, err := sandbox.adapter.BindSource(request.SkillRoot)
	if err != nil {
		return result, &Error{Code: "SKILL_SCRIPT_PATH_UNSAFE", Message: err.Error()}
	}
	outputRoot, err := filepath.Abs(request.OutputRoot)
	if err != nil {
		return result, &Error{Code: "SKILL_SCRIPT_OUTPUT_UNSAFE", Message: err.Error()}
	}
	if pathsOverlap(skillRoot, workRoot) || pathsOverlap(skillRoot, outputRoot) || pathsOverlap(workRoot, outputRoot) {
		return result, &Error{Code: "SKILL_SCRIPT_WORKSPACE_UNSAFE", Message: "脚本工作区、Skill 包和输出目录不得重叠。"}
	}
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		return result, err
	}
	inputRoot := filepath.Join(workRoot, "input")
	if err := os.Mkdir(inputRoot, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return result, err
	}
	inputFile := filepath.Join(inputRoot, "input.json")
	if err := os.WriteFile(inputFile, request.Input, 0o444); err != nil {
		return result, err
	}
	inputBind, err := sandbox.adapter.BindSource(inputRoot)
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(outputRoot, 0o700); err != nil {
		return result, err
	}

	containerName := "content-agent-script-" + strings.ToLower(request.ExecutionID)
	createArgs := sandbox.createArguments(containerName, skillRoot, inputBind, request.Limits)
	createCtx, cancelCreate := context.WithTimeout(ctx, engineOperationTimeout)
	create, err := sandbox.runner.Run(createCtx, sandbox.enginePath, createArgs, 4096, defaultSummaryBytes)
	if err != nil || create.ExitCode != 0 {
		if err == nil {
			err = errors.New("container creation failed")
		}
		classified := classifyEngineError(createCtx, "SCRIPT_SANDBOX_CREATE_FAILED", create, err)
		cancelCreate()
		return result, classified
	}
	cancelCreate()
	containerID := strings.TrimSpace(string(create.Stdout))
	if !containerIDPattern.MatchString(containerID) {
		return result, &Error{Code: "SCRIPT_SANDBOX_CREATE_FAILED", Message: "Container creation returned no confirmed identity; an unknown container was not removed."}
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cleanup, cleanupErr := sandbox.runner.Run(
			cleanupCtx, sandbox.enginePath, []string{"rm", "--force", containerID}, 4096, 4096,
		)
		if cleanupErr != nil || cleanup.ExitCode != 0 {
			message := sanitizeSummary(cleanup.Stderr)
			if message == "" {
				message = "脚本容器清理失败，执行环境可能仍然存在。"
			}
			returnedErr = &Error{Code: "SCRIPT_SANDBOX_CLEANUP_FAILED", Message: message}
		}
	}()

	executionCtx, cancel := context.WithTimeout(ctx, time.Duration(request.Limits.TimeoutSeconds)*time.Second)
	defer cancel()
	started, startErr := sandbox.runner.Run(executionCtx, sandbox.enginePath, []string{"start", containerID}, 4096, defaultSummaryBytes)
	if startErr != nil || started.ExitCode != 0 {
		if startErr == nil {
			startErr = errors.New("container start failed")
		}
		return result, classifyEngineError(executionCtx, "SCRIPT_SANDBOX_START_FAILED", started, startErr)
	}
	run, runErr := sandbox.runner.Run(
		executionCtx, sandbox.enginePath, scriptExecArguments(containerID, scriptPath),
		defaultSummaryBytes, defaultSummaryBytes,
	)
	result.ExitCode = run.ExitCode
	result.StdoutSummary = sanitizeSummary(run.Stdout)
	result.StderrSummary = sanitizeSummary(run.Stderr)
	if executionCtx.Err() != nil {
		result.CompletedAt = time.Now().UTC()
		return result, &Error{Code: "SKILL_SCRIPT_TIMEOUT", Message: "脚本执行超过时间限制。"}
	}
	if runErr != nil && (run.ExitCode <= 0 || ErrorCode(runErr) == "SKILL_SCRIPT_OUTPUT_LIMIT_EXCEEDED") {
		result.CompletedAt = time.Now().UTC()
		return result, classifyEngineError(executionCtx, "SKILL_SCRIPT_EXECUTION_FAILED", run, runErr)
	}

	archiveLimit := request.Limits.DiskBytes + (2 << 20)
	exportCtx, cancelExport := context.WithTimeout(ctx, engineOperationTimeout)
	archive, copyErr := sandbox.runner.Run(
		exportCtx, sandbox.enginePath, []string{"exec", "--user", workspaceUser, "--workdir", "/work", containerID,
			"/usr/bin/env", "-i", "--", "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "HOME=/work",
			"python", "-I", "-B", "-c", workspaceTransferHelper, "export-output", strconv.FormatInt(request.Limits.DiskBytes, 10)},
		archiveLimit, defaultSummaryBytes,
	)
	if copyErr != nil || archive.ExitCode != 0 || len(archive.Stderr) != 0 {
		result.CompletedAt = time.Now().UTC()
		cancelExport()
		code := "SKILL_SCRIPT_OUTPUT_EXPORT_FAILED"
		var receipt struct {
			Code string `json:"code"`
		}
		if archive.ExitCode == 1 && json.Unmarshal(archive.Stderr, &receipt) == nil {
			switch receipt.Code {
			case "WORKSPACE_ARCHIVE_ENTRY_UNSAFE":
				code = "SKILL_SCRIPT_OUTPUT_ENTRY_UNSAFE"
			case "WORKSPACE_ARCHIVE_PATH_UNSAFE":
				code = "SKILL_SCRIPT_OUTPUT_PATH_UNSAFE"
			case "WORKSPACE_ARCHIVE_PATH_COLLISION":
				code = "SKILL_SCRIPT_OUTPUT_PATH_COLLISION"
			case "WORKSPACE_ARCHIVE_LIMIT_EXCEEDED":
				code = "SKILL_SCRIPT_OUTPUT_LIMIT_EXCEEDED"
			}
		}
		return result, &Error{Code: code, Message: "Script output export was not confirmed; no artifacts were accepted."}
	}
	cancelExport()
	snapshot, snapshotErr := ParseWorkspaceSnapshot(archive.Stdout, request.Limits)
	if snapshotErr != nil {
		return result, &Error{Code: "SKILL_SCRIPT_OUTPUT_ARCHIVE_INVALID", Message: "Script output archive failed integrity validation."}
	}
	artifacts, extractErr := extractArtifactArchive(snapshot.Archive, outputRoot, request.Limits.DiskBytes)
	result.Artifacts = artifacts
	result.CompletedAt = time.Now().UTC()
	if extractErr != nil {
		return result, extractErr
	}
	if runErr != nil || run.ExitCode != 0 {
		return result, &Error{
			Code:    "SKILL_SCRIPT_EXIT_NONZERO",
			Message: fmt.Sprintf("脚本以退出码 %d 结束。", run.ExitCode),
		}
	}
	return result, nil
}

func pathsOverlap(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	for _, pair := range [][2]string{{left, right}, {right, left}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (sandbox *OCI) createArguments(
	containerName string,
	skillRoot string,
	inputRoot string,
	limits Limits,
) []string {
	memory := strconv.FormatInt(limits.MemoryBytes, 10)
	disk := strconv.FormatInt(limits.DiskBytes, 10)
	temp := strconv.FormatInt(limits.TempBytes, 10)
	return []string{
		"create",
		"--pull", "never",
		"--name", containerName,
		"--hostname", "skill-sandbox",
		"--network", "none",
		"--ipc", "none",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--pids-limit", strconv.Itoa(limits.ProcessCount),
		"--memory", memory,
		"--memory-swap", memory,
		"--cpus", strconv.FormatFloat(limits.CPUCount, 'f', -1, 64),
		"--ulimit", "nofile=64:64",
		"--ulimit", "core=0:0",
		"--user", "65532:65532",
		"--workdir", "/work",
		"--log-driver", "none",
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=" + temp + ",mode=1777",
		"--tmpfs", "/work:rw,noexec,nosuid,nodev,size=" + temp + ",mode=1777",
		"--tmpfs", "/output:rw,noexec,nosuid,nodev,size=" + disk + ",mode=1777",
		"--mount", "type=bind,source=" + skillRoot + ",target=/skill,readonly",
		"--mount", "type=bind,source=" + inputRoot + ",target=/input,readonly",
		"--entrypoint", "/usr/bin/env",
		sandbox.pythonImage,
		"-i",
		"--",
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"LANG=C.UTF-8",
		"HOME=/work",
		"python", "-I", "-B", "-c", workspaceSupervisor,
	}
}

func scriptExecArguments(containerID, scriptPath string) []string {
	return []string{
		"exec", "--user", workspaceUser, "--workdir", "/work", containerID,
		"/usr/bin/env", "-i", "--", "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "HOME=/work",
		"PYTHONHASHSEED=0",
		"CONTENT_AGENT_INPUT=/input/input.json",
		"CONTENT_AGENT_OUTPUT=/output",
		"python", "-I", "-B", "/skill/" + filepath.ToSlash(scriptPath), "/input/input.json", "/output",
	}
}

func validateScriptPath(skillRoot, reference string) (string, error) {
	if reference == "" || strings.Contains(reference, "\\") || path.IsAbs(reference) ||
		path.Clean(reference) != reference || !strings.HasPrefix(reference, "scripts/") ||
		strings.ToLower(path.Ext(reference)) != ".py" {
		return "", errors.New("script path must be a normalized .py path under scripts/")
	}
	root, err := filepath.Abs(skillRoot)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(reference)))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("script path escapes the Skill package")
	}
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("script path must reference a regular file")
	}
	return filepath.ToSlash(relative), nil
}

func extractArtifactArchive(data []byte, outputRoot string, diskLimit int64) ([]Artifact, error) {
	reader := tar.NewReader(bytes.NewReader(data))
	artifacts := make([]Artifact, 0)
	seen := make(map[string]struct{})
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return artifacts, &Error{Code: "SKILL_SCRIPT_OUTPUT_ARCHIVE_INVALID", Message: "脚本输出归档无效。"}
		}
		name := strings.TrimPrefix(strings.ReplaceAll(header.Name, "\\", "/"), "./")
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return artifacts, &Error{Code: "SKILL_SCRIPT_OUTPUT_ENTRY_UNSAFE", Message: "脚本输出只能包含普通文件。"}
		}
		if !safeArtifactPath(name) {
			return artifacts, &Error{Code: "SKILL_SCRIPT_OUTPUT_PATH_UNSAFE", Message: "脚本输出包含不安全路径。"}
		}
		key := strings.ToLower(name)
		if _, duplicate := seen[key]; duplicate {
			return artifacts, &Error{Code: "SKILL_SCRIPT_OUTPUT_PATH_COLLISION", Message: "脚本输出包含重复路径。"}
		}
		seen[key] = struct{}{}
		if len(seen) > maxArtifactFiles || header.Size < 0 || header.Size > diskLimit-total {
			return artifacts, &Error{Code: "SKILL_SCRIPT_DISK_LIMIT_EXCEEDED", Message: "脚本输出超过磁盘或文件数量限制。"}
		}
		total += header.Size
		target := filepath.Join(outputRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return artifacts, err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return artifacts, err
		}
		hash := sha256.New()
		written, copyErr := io.CopyN(io.MultiWriter(file, hash), reader, header.Size)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || written != header.Size {
			return artifacts, errors.Join(copyErr, closeErr)
		}
		artifacts = append(artifacts, Artifact{
			Path: name, SizeBytes: written, SHA256: "sha256:" + hex.EncodeToString(hash.Sum(nil)),
		})
	}
	sort.Slice(artifacts, func(left, right int) bool { return artifacts[left].Path < artifacts[right].Path })
	return artifacts, nil
}

func safeArtifactPath(name string) bool {
	if name == "" || len(name) > 240 || path.IsAbs(name) || path.Clean(name) != name ||
		strings.ContainsAny(name, "\x00:\r\n") {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		trimmed := strings.TrimRight(segment, ". ")
		if trimmed == "" || trimmed != segment || windowsDeviceName(segment) {
			return false
		}
	}
	return true
}

func windowsDeviceName(value string) bool {
	base := strings.ToUpper(strings.TrimSuffix(value, path.Ext(value)))
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
		base[3] >= '1' && base[3] <= '9'
}

func classifyEngineError(ctx context.Context, code string, result commandResult, err error) error {
	if ctx.Err() != nil {
		return &Error{Code: "SKILL_SCRIPT_TIMEOUT", Message: "脚本沙箱命令超时或被取消。"}
	}
	message := sanitizeSummary(result.Stderr)
	if message == "" {
		message = err.Error()
	}
	return &Error{Code: code, Message: message}
}

func sanitizeSummary(data []byte) string {
	text := strings.ToValidUTF8(string(data), "?")
	text = strings.ReplaceAll(text, "\x00", "")
	return strings.TrimSpace(text)
}
