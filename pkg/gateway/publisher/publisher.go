package publisher

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	maxSnapshotBytes         = 8 << 20
	maxSnapshotExpandedBytes = 256 << 20
	maxSnapshotEntries       = 100_000
	maxBuildLogBytes         = 1 << 20
)

var (
	idPattern                  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	repositoryPattern          = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*(?::[0-9]{1,5})?/[a-z0-9]+(?:[._/-][a-z0-9]+)*$`)
	repositoryComponentPattern = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
	digestPattern              = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

type Config struct {
	BuildctlPath     string
	Address          string
	RepositoryPrefix string
	Platform         string
	Timeout          time.Duration
	CACert           string
	ClientCert       string
	ClientKey        string
	DockerConfig     string
	AllowInsecure    bool
}

type Request struct {
	ProjectID  string
	ReleaseID  string
	Context    string
	Dockerfile string
	Snapshot   []byte
}

type Result struct {
	ImageRef string `json:"image_ref"`
	Digest   string `json:"digest"`
	Logs     string `json:"logs"`
}

type BuildError struct {
	Err  error
	Logs string
}

func (e *BuildError) Error() string { return e.Err.Error() }
func (e *BuildError) Unwrap() error { return e.Err }

type Publisher struct {
	config Config
	run    func(context.Context, string, []string, []string, io.Writer) error
}

func New(config Config) (*Publisher, error) {
	config.BuildctlPath = strings.TrimSpace(config.BuildctlPath)
	if config.BuildctlPath == "" {
		config.BuildctlPath = "buildctl"
	}
	config.Address = strings.TrimSpace(config.Address)
	config.RepositoryPrefix = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(config.RepositoryPrefix)), "/")
	config.Platform = strings.TrimSpace(config.Platform)
	if config.Platform == "" {
		config.Platform = "linux/amd64"
	}
	if config.Timeout <= 0 {
		config.Timeout = 20 * time.Minute
	}
	if config.Address == "" || !repositoryPattern.MatchString(config.RepositoryPrefix) {
		return nil, errors.New("buildkit address and a valid registry repository prefix are required")
	}
	if !strings.HasPrefix(config.Address, "tcp://") && !strings.HasPrefix(config.Address, "unix://") {
		return nil, errors.New("BuildKit address must use tcp:// or unix://")
	}
	if !strings.HasPrefix(config.Platform, "linux/") || strings.ContainsAny(config.Platform, ", ") {
		return nil, errors.New("publisher platform must be one Linux platform")
	}
	if (config.ClientCert == "") != (config.ClientKey == "") {
		return nil, errors.New("BuildKit client certificate and key must be configured together")
	}
	if strings.HasPrefix(config.Address, "tcp://") && !config.AllowInsecure && (config.CACert == "" || config.ClientCert == "" || config.ClientKey == "") {
		return nil, errors.New("remote BuildKit requires a CA and client certificate unless insecure transport is explicitly enabled")
	}
	for name, path := range map[string]string{"BuildKit CA": config.CACert, "BuildKit client certificate": config.ClientCert, "BuildKit client key": config.ClientKey} {
		if path != "" {
			if err := validateReadableFile(path); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	if config.DockerConfig != "" {
		if err := validateReadableFile(filepath.Join(config.DockerConfig, "config.json")); err != nil {
			return nil, fmt.Errorf("registry Docker config: %w", err)
		}
	}
	return &Publisher{config: config, run: runCommand}, nil
}

func validateReadableFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || !info.Mode().IsRegular() {
		return errors.Join(statErr, errors.New("must be a regular file"))
	}
	return closeErr
}

func (p *Publisher) Build(ctx context.Context, request Request) (*Result, error) {
	if !idPattern.MatchString(request.ProjectID) || !idPattern.MatchString(request.ReleaseID) {
		return nil, errors.New("invalid project or release identifier")
	}
	projectComponent := strings.ToLower(request.ProjectID)
	if !repositoryComponentPattern.MatchString(projectComponent) {
		return nil, errors.New("project identifier cannot form an OCI repository name")
	}
	if len(request.Snapshot) == 0 || len(request.Snapshot) > maxSnapshotBytes {
		return nil, fmt.Errorf("workspace snapshot must be between 1 and %d bytes", maxSnapshotBytes)
	}
	buildCtx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	root, err := os.MkdirTemp("", "agentland-publish-")
	if err != nil {
		return nil, fmt.Errorf("create build directory: %w", err)
	}
	defer os.RemoveAll(root)
	if err := extractSnapshot(buildCtx, root, request.Snapshot); err != nil {
		if errors.Is(buildCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("image build exceeded %s", p.config.Timeout)
		}
		return nil, err
	}

	contextDir, err := resolveBuildPath(root, request.Context, true)
	if err != nil {
		return nil, fmt.Errorf("resolve build context: %w", err)
	}
	dockerfilePath, err := resolveBuildPath(contextDir, request.Dockerfile, false)
	if err != nil {
		return nil, fmt.Errorf("resolve Dockerfile: %w", err)
	}
	info, err := os.Stat(dockerfilePath)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("Dockerfile is missing or is not a regular file")
	}

	imageRef := p.config.RepositoryPrefix + "/" + projectComponent + ":" + request.ReleaseID
	cacheRef := p.config.RepositoryPrefix + "/" + projectComponent + ":buildcache"
	metadataPath := filepath.Join(root, "metadata.json")
	args := []string{"--addr", p.config.Address}
	if p.config.CACert != "" {
		args = append(args, "--tlscacert", p.config.CACert)
	}
	if p.config.ClientCert != "" {
		args = append(args, "--tlscert", p.config.ClientCert, "--tlskey", p.config.ClientKey)
	}
	args = append(args,
		"build", "--frontend", "dockerfile.v0", "--progress", "plain",
		"--local", "context="+contextDir,
		"--local", "dockerfile="+filepath.Dir(dockerfilePath),
		"--opt", "filename="+filepath.Base(dockerfilePath),
		"--opt", "platform="+p.config.Platform,
		"--output", "type=image,name="+imageRef+",push=true,oci-mediatypes=true",
		"--import-cache", "type=registry,ref="+cacheRef,
		"--export-cache", "type=registry,ref="+cacheRef+",mode=max,oci-mediatypes=true,image-manifest=true",
		"--metadata-file", metadataPath,
	)

	logs := &tailWriter{limit: maxBuildLogBytes}
	env := []string{"BUILDKIT_PROGRESS=plain"}
	if p.config.DockerConfig != "" {
		env = append(env, "DOCKER_CONFIG="+p.config.DockerConfig)
	}
	if err := p.run(buildCtx, p.config.BuildctlPath, args, env, logs); err != nil {
		if errors.Is(buildCtx.Err(), context.DeadlineExceeded) {
			return nil, &BuildError{Err: fmt.Errorf("image build exceeded %s", p.config.Timeout), Logs: logs.String()}
		}
		return nil, &BuildError{Err: fmt.Errorf("build and push image: %w", err), Logs: logs.String()}
	}
	digest, err := readDigest(metadataPath)
	if err != nil {
		return nil, &BuildError{Err: err, Logs: logs.String()}
	}
	return &Result{ImageRef: imageRef, Digest: digest, Logs: logs.String()}, nil
}

func runCommand(ctx context.Context, executable string, args, extraEnv []string, output io.Writer) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = append(commandEnvironment(), extraEnv...)
	command.Stdout, command.Stderr = output, output
	return command.Run()
}

func commandEnvironment() []string {
	keys := []string{"PATH", "HOME", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"}
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			environment = append(environment, key+"="+value)
		}
	}
	return environment
}

func readDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read BuildKit metadata: %w", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		return "", fmt.Errorf("decode BuildKit metadata: %w", err)
	}
	for _, key := range []string{"containerimage.digest", "containerimage.config.digest"} {
		if digest, ok := metadata[key].(string); ok && digestPattern.MatchString(digest) {
			return digest, nil
		}
	}
	return "", errors.New("BuildKit did not return an image digest")
}

func resolveBuildPath(root, relative string, directory bool) (string, error) {
	relative = strings.TrimSpace(relative)
	if relative == "" {
		if directory {
			relative = "."
		} else {
			relative = "Dockerfile"
		}
	}
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(filepath.Clean(relative), ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the workspace")
	}
	resolved := filepath.Join(root, filepath.Clean(relative))
	real, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil || real != rootReal && !strings.HasPrefix(real, rootReal+string(filepath.Separator)) {
		return "", errors.New("path escapes the workspace")
	}
	if directory {
		info, err := os.Stat(real)
		if err != nil || !info.IsDir() {
			return "", errors.New("build context is not a directory")
		}
	}
	return real, nil
}

func extractSnapshot(ctx context.Context, destination string, data []byte) error {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open workspace snapshot: %w", err)
	}
	defer reader.Close()
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	tarReader := tar.NewReader(&contextReader{ctx: ctx, reader: reader})
	var expanded int64
	entries := 0
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read workspace snapshot: %w", err)
		}
		entries++
		if entries > maxSnapshotEntries || header.Size < 0 || expanded > maxSnapshotExpandedBytes-header.Size {
			return errors.New("workspace snapshot exceeds extraction limits")
		}
		expanded += header.Size
		name, err := cleanArchiveName(header.Name)
		if err != nil {
			return err
		}
		if name == "." {
			continue
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(header.Mode)&0o755 | 0o600
			file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, tarReader, header.Size)
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported workspace snapshot entry %q", header.Name)
		}
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(data []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(data)
	}
}

func cleanArchiveName(name string) (string, error) {
	name = filepath.Clean(filepath.FromSlash(name))
	if name == "" || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace snapshot path %q escapes the build directory", name)
	}
	return name, nil
}

type tailWriter struct {
	mu        sync.Mutex
	limit     int
	data      []byte
	discarded int64
}

func (w *tailWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := len(data)
	w.data = append(w.data, data...)
	if len(w.data) > w.limit {
		remove := len(w.data) - w.limit
		w.data = append([]byte(nil), w.data[remove:]...)
		w.discarded += int64(remove)
	}
	return written, nil
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	content := strings.ToValidUTF8(string(w.data), "\uFFFD")
	content = strings.ReplaceAll(content, "\x00", "\uFFFD")
	if w.discarded != 0 {
		return fmt.Sprintf("[earlier build log truncated: %d bytes]\n%s", w.discarded, content)
	}
	return content
}
