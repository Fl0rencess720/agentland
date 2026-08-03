package agentd

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxToolOutputBytes      = 256 << 10
	maxToolOutputEventBytes = 64 << 10
)

func boundToolOutput(output string) string {
	return boundToolOutputSize(output, int64(len(output)))
}

func boundToolOutputSize(output string, originalBytes int64) string {
	sample := output
	truncated := originalBytes > maxToolOutputBytes || len(sample) > maxToolOutputBytes
	if len(sample) > maxToolOutputBytes {
		sample = sample[:maxToolOutputBytes]
	}
	sample = strings.ToValidUTF8(sample, "\uFFFD")
	if !truncated && len(sample) <= maxToolOutputBytes {
		return sample
	}

	marker := fmt.Sprintf("\n\n[tool output truncated; original size: %d bytes]", originalBytes)
	return utf8Prefix(sample, maxToolOutputBytes-len(marker)) + marker
}

func utf8Prefix(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}
