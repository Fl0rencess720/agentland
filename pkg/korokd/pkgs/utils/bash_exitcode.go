package utils

import (
	"strconv"
	"strings"
)

const BashExitMarkerPrefix = "__AGENTLAND_EXITCODE__"

type BashExitCodeFilter struct {
	markerPrefix string
	keepTail     int

	buf      string
	exitCode *int32
}

func NewBashExitCodeFilter(markerKey string) *BashExitCodeFilter {
	return &BashExitCodeFilter{
		markerPrefix: markerKey + "=",
		keepTail:     512,
	}
}

func (f *BashExitCodeFilter) HandleChunk(chunk string) string {
	if chunk == "" {
		return ""
	}
	f.buf += chunk
	return f.drain(false)
}

func (f *BashExitCodeFilter) Flush() string {
	return f.drain(true)
}

func (f *BashExitCodeFilter) ExitCode() (int32, bool) {
	if f.exitCode == nil {
		return 0, false
	}
	return *f.exitCode, true
}

func (f *BashExitCodeFilter) drain(final bool) string {
	var out strings.Builder

	for {
		idx := strings.Index(f.buf, f.markerPrefix)
		if idx >= 0 {
			out.WriteString(f.buf[:idx])
			f.buf = f.buf[idx:]
			rest := f.buf[len(f.markerPrefix):]
			valueEnd, newlineLen := scanLineEnd(rest)
			if newlineLen == 0 && !final {
				break
			}

			if parsed, ok := parseLeadingInt32(rest[:valueEnd]); ok {
				f.exitCode = &parsed
			}
			if valueEnd+newlineLen >= len(rest) {
				f.buf = ""
			} else {
				f.buf = rest[valueEnd+newlineLen:]
			}
			continue
		}

		if !final && len(f.buf) > f.keepTail {
			cut := len(f.buf) - f.keepTail
			out.WriteString(f.buf[:cut])
			f.buf = f.buf[cut:]
		}
		break
	}

	if final && f.buf != "" {
		out.WriteString(f.buf)
		f.buf = ""
	}

	return out.String()
}

func scanLineEnd(s string) (valueEnd int, newlineLen int) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n':
			return i, 1
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				return i, 2
			}
			return i, 1
		default:
		}
	}
	return len(s), 0
}

func parseLeadingInt32(s string) (int32, bool) {
	v := strings.TrimSpace(s)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return int32(n), true
}

func ParseExitMarker(stdout, markerKey string) (int32, bool) {
	prefix := markerKey + "="
	idx := strings.LastIndex(stdout, prefix)
	if idx < 0 {
		return 0, false
	}
	rest := stdout[idx+len(prefix):]
	valueEnd, _ := scanLineEnd(rest)
	return parseLeadingInt32(rest[:valueEnd])
}

func StripExitMarker(stdout, markerKey string) string {
	prefix := markerKey + "="
	idx := strings.LastIndex(stdout, prefix)
	if idx < 0 {
		return stdout
	}

	rest := stdout[idx+len(prefix):]
	valueEnd, newlineLen := scanLineEnd(rest)
	cutEnd := idx + len(prefix) + valueEnd + newlineLen
	if cutEnd > len(stdout) {
		cutEnd = len(stdout)
	}
	return stdout[:idx] + stdout[cutEnd:]
}
