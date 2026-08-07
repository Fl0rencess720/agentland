//go:build linux

package agentd

import (
	"fmt"
	"os"
)

func fileDescriptorPath(file *os.File) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
}
