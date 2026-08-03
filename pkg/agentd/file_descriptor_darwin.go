//go:build darwin

package agentd

import (
	"bytes"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func fileDescriptorPath(file *os.File) (string, error) {
	var path [4096]byte
	_, _, errno := syscall.Syscall(
		syscall.SYS_FCNTL,
		file.Fd(),
		syscall.F_GETPATH,
		uintptr(unsafe.Pointer(&path[0])),
	)
	if errno != 0 {
		return "", fmt.Errorf("fcntl F_GETPATH: %w", errno)
	}
	if end := bytes.IndexByte(path[:], 0); end >= 0 {
		return string(path[:end]), nil
	}
	return "", fmt.Errorf("fcntl F_GETPATH returned an unterminated path")
}
