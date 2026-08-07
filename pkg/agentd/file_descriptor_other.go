//go:build !linux && !darwin

package agentd

import (
	"os"
	"path/filepath"
)

func fileDescriptorPath(file *os.File) (string, error) {
	return filepath.EvalSymlinks(file.Name())
}
