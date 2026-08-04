package agentd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	maxWorkspaceSnapshotBytes         = 8 << 20
	maxWorkspaceSnapshotExpandedBytes = 256 << 20
	maxWorkspaceSnapshotEntries       = 100_000
)

type workspaceSnapshotHandler struct {
	tools *localTools
}

func (h *workspaceSnapshotHandler) download(c *gin.Context) {
	h.tools.writeMu.Lock()
	data, err := createWorkspaceSnapshot(h.tools.root)
	h.tools.writeMu.Unlock()
	if err != nil {
		if errors.Is(err, errSnapshotTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create workspace snapshot failed"})
		return
	}
	c.Header("Content-Type", "application/gzip")
	c.Header("Content-Disposition", `attachment; filename="workspace.tar.gz"`)
	c.Data(http.StatusOK, "application/gzip", data)
}

func (h *workspaceSnapshotHandler) restore(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxWorkspaceSnapshotBytes)
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "workspace snapshot is too large"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "read workspace snapshot failed"})
		return
	}
	if len(data) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workspace snapshot is required"})
		return
	}
	h.tools.writeMu.Lock()
	err = restoreWorkspaceSnapshot(h.tools.root, data)
	h.tools.writeMu.Unlock()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"restored": true})
}

var errSnapshotTooLarge = errors.New("workspace snapshot exceeds 8 MiB")

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (w *limitedBuffer) Write(data []byte) (int, error) {
	if w.Len()+len(data) > w.limit {
		return 0, errSnapshotTooLarge
	}
	return w.Buffer.Write(data)
}

func createWorkspaceSnapshot(workspaceRoot string) ([]byte, error) {
	root, err := os.OpenRoot(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("open workspace: %w", err)
	}
	defer root.Close()
	output := &limitedBuffer{limit: maxWorkspaceSnapshotBytes}
	gzipWriter := gzip.NewWriter(output)
	tarWriter := tar.NewWriter(gzipWriter)
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if entry.IsDir() {
			if _, ignored := workspaceTreeIgnoredDirs[entry.Name()]; ignored {
				return filepath.SkipDir
			}
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !entry.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(name)
		header.Mode = int64(snapshotMode(info))
		header.Uid, header.Gid = 0, 0
		header.Uname, header.Gname = "", ""
		header.ModTime = time.Unix(0, 0).UTC()
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, file)
		closeErr := file.Close()
		return errors.Join(copyErr, closeErr)
	})
	if err == nil {
		err = tarWriter.Close()
	} else {
		_ = tarWriter.Close()
	}
	if err == nil {
		err = gzipWriter.Close()
	} else {
		_ = gzipWriter.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("archive workspace: %w", err)
	}
	return output.Bytes(), nil
}

func snapshotMode(info fs.FileInfo) os.FileMode {
	if info.IsDir() {
		return 0o755
	}
	mode := os.FileMode(0o644)
	if info.Mode()&0o111 != 0 {
		mode |= 0o111
	}
	return mode
}

func restoreWorkspaceSnapshot(workspaceRoot string, data []byte) error {
	staging, err := os.MkdirTemp(filepath.Dir(workspaceRoot), ".agentd-restore-")
	if err != nil {
		return fmt.Errorf("create restore directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := extractWorkspaceSnapshot(staging, data); err != nil {
		return err
	}

	root, err := os.OpenRoot(workspaceRoot)
	if err != nil {
		return fmt.Errorf("open workspace: %w", err)
	}
	entries, err := os.ReadDir(workspaceRoot)
	if err != nil {
		root.Close()
		return fmt.Errorf("list workspace: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() == ".agentland" {
			continue
		}
		if err := root.RemoveAll(entry.Name()); err != nil {
			root.Close()
			return fmt.Errorf("clear workspace entry %s: %w", entry.Name(), err)
		}
	}
	if err := root.Close(); err != nil {
		return err
	}
	stagedEntries, err := os.ReadDir(staging)
	if err != nil {
		return fmt.Errorf("list restored workspace: %w", err)
	}
	for _, entry := range stagedEntries {
		if err := os.Rename(filepath.Join(staging, entry.Name()), filepath.Join(workspaceRoot, entry.Name())); err != nil {
			return fmt.Errorf("install restored entry %s: %w", entry.Name(), err)
		}
	}
	if err := assignWorkspaceOwnership(workspaceRoot); err != nil {
		return err
	}
	return nil
}

func extractWorkspaceSnapshot(destination string, data []byte) error {
	gzipReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open workspace snapshot: %w", err)
	}
	defer gzipReader.Close()
	root, err := os.OpenRoot(destination)
	if err != nil {
		return fmt.Errorf("open restore directory: %w", err)
	}
	defer root.Close()
	tarReader := tar.NewReader(gzipReader)
	var totalSize int64
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
		if entries > maxWorkspaceSnapshotEntries {
			return fmt.Errorf("workspace snapshot exceeds %d entries", maxWorkspaceSnapshotEntries)
		}
		if header.Size < 0 || totalSize > maxWorkspaceSnapshotExpandedBytes-header.Size {
			return fmt.Errorf("expanded workspace snapshot exceeds %d bytes", maxWorkspaceSnapshotExpandedBytes)
		}
		totalSize += header.Size
		name, err := safeSnapshotName(header.Name)
		if err != nil {
			return err
		}
		if name == "." {
			continue
		}
		mode := os.FileMode(header.Mode) & 0o777
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, mode|0o700); err != nil {
				return fmt.Errorf("create restored directory %s: %w", name, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return fmt.Errorf("create restored parent %s: %w", name, err)
			}
			file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode|0o600)
			if err != nil {
				return fmt.Errorf("create restored file %s: %w", name, err)
			}
			_, copyErr := io.CopyN(file, tarReader, header.Size)
			chmodErr := file.Chmod(mode)
			closeErr := file.Close()
			if err := errors.Join(copyErr, chmodErr, closeErr); err != nil {
				return fmt.Errorf("write restored file %s: %w", name, err)
			}
		default:
			return fmt.Errorf("snapshot entry %s has unsupported type", name)
		}
	}
	return nil
}

func safeSnapshotName(raw string) (string, error) {
	if raw == "" || strings.ContainsRune(raw, 0) {
		return "", errors.New("snapshot path is invalid")
	}
	name := filepath.Clean(filepath.FromSlash(raw))
	if name == "." {
		return name, nil
	}
	if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("snapshot path %q escapes workspace", raw)
	}
	if err := ensureInternalAccess(name, false); err != nil {
		return "", fmt.Errorf("snapshot path %q is reserved", raw)
	}
	return name, nil
}

func assignWorkspaceOwnership(workspaceRoot string) error {
	uid, gid, configured, err := configuredToolIdentity()
	if err != nil || !configured {
		return err
	}
	if os.Geteuid() != 0 {
		if os.Geteuid() == int(uid) && os.Getegid() == int(gid) {
			return nil
		}
		return fmt.Errorf("agentd must run as root to assign restored workspace ownership")
	}
	return filepath.WalkDir(workspaceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != workspaceRoot && entry.Name() == ".agentland" && entry.IsDir() {
			return filepath.SkipDir
		}
		return os.Chown(path, int(uid), int(gid))
	})
}
