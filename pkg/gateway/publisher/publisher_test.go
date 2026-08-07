package publisher

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

type archiveEntry struct {
	name     string
	typeflag byte
	content  string
}

func TestBuildPushesImmutableImageAndRegistryCache(t *testing.T) {
	credentials := t.TempDir()
	for _, name := range []string{"ca.pem", "cert.pem", "key.pem", "config.json"} {
		if err := os.WriteFile(credentials+string(os.PathSeparator)+name, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := New(Config{
		Address: "tcp://buildkit.example:1234", RepositoryPrefix: "registry.example/team/apps",
		CACert: credentials + "/ca.pem", ClientCert: credentials + "/cert.pem", ClientKey: credentials + "/key.pem", DockerConfig: credentials,
	})
	if err != nil {
		t.Fatal(err)
	}
	var command string
	var args, env []string
	p.run = func(_ context.Context, executable string, actualArgs, actualEnv []string, output io.Writer) error {
		command, args, env = executable, append([]string(nil), actualArgs...), append([]string(nil), actualEnv...)
		_, _ = io.WriteString(output, "build complete\n")
		metadata := argumentValue(actualArgs, "--metadata-file")
		data, _ := json.Marshal(map[string]string{"containerimage.digest": "sha256:" + strings.Repeat("a", 64)})
		return os.WriteFile(metadata, data, 0o600)
	}
	result, err := p.Build(context.Background(), Request{
		ProjectID: "project_123", ReleaseID: "pub_456", Context: ".", Dockerfile: "Dockerfile",
		Snapshot: snapshot(t, archiveEntry{name: "Dockerfile", typeflag: tar.TypeReg, content: "FROM scratch\n"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if command != "buildctl" || result.ImageRef != "registry.example/team/apps/project_123:pub_456" {
		t.Fatalf("unexpected build result: command=%q result=%+v", command, result)
	}
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"--addr tcp://buildkit.example:1234", "--tlscacert " + credentials + "/ca.pem", "--tlscert " + credentials + "/cert.pem", "--tlskey " + credentials + "/key.pem",
		"type=image,name=registry.example/team/apps/project_123:pub_456,push=true", "type=registry,ref=registry.example/team/apps/project_123:buildcache",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("build arguments do not contain %q: %s", expected, joined)
		}
	}
	if !contains(env, "DOCKER_CONFIG="+credentials) || result.Digest != "sha256:"+strings.Repeat("a", 64) || result.Logs != "build complete\n" {
		t.Fatalf("unexpected environment or result: env=%v result=%+v", env, result)
	}
}

func TestNewRequiresMutualTLSForRemoteBuildKit(t *testing.T) {
	_, err := New(Config{Address: "tcp://buildkit.example:1234", RepositoryPrefix: "registry.example/team"})
	if err == nil || !strings.Contains(err.Error(), "requires a CA and client certificate") {
		t.Fatalf("expected remote BuildKit TLS validation, got %v", err)
	}
}

func TestBuildRejectsUnsafeWorkspaceSnapshot(t *testing.T) {
	p, err := New(Config{Address: "tcp://buildkit:1234", RepositoryPrefix: "registry.example/team", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	p.run = func(context.Context, string, []string, []string, io.Writer) error { called = true; return nil }
	_, err = p.Build(context.Background(), Request{
		ProjectID: "project_1", ReleaseID: "pub_1",
		Snapshot: snapshot(t, archiveEntry{name: "Dockerfile", typeflag: tar.TypeReg, content: "FROM scratch"}, archiveEntry{name: "escape", typeflag: tar.TypeSymlink}),
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported workspace snapshot entry") || called {
		t.Fatalf("unsafe archive was accepted: called=%v err=%v", called, err)
	}
}

func TestBuildCancelsCommandAtTimeout(t *testing.T) {
	p, err := New(Config{Address: "tcp://buildkit:1234", RepositoryPrefix: "registry.example/team", Timeout: 10 * time.Millisecond, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	p.run = func(ctx context.Context, _ string, _ []string, _ []string, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}
	_, err = p.Build(context.Background(), Request{
		ProjectID: "project_1", ReleaseID: "pub_1",
		Snapshot: snapshot(t, archiveEntry{name: "Dockerfile", typeflag: tar.TypeReg, content: "FROM scratch"}),
	})
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	var buildErr *BuildError
	if !errors.As(err, &buildErr) {
		t.Fatalf("expected structured build error, got %T", err)
	}
}

func snapshot(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Typeflag: entry.typeflag, Mode: 0o644, Size: int64(len(entry.content))}
		if entry.typeflag == tar.TypeSymlink {
			header.Linkname, header.Size = "../../outside", 0
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if entry.content != "" {
			if _, err := io.WriteString(tarWriter, entry.content); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func argumentValue(args []string, key string) string {
	for index := range args {
		if args[index] == key && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
