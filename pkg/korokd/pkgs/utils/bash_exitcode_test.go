package utils

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAndStripExitMarker(t *testing.T) {
	markerKey := BashExitMarkerPrefix + "token-1"
	stdout := "hello\n" + markerKey + "=7\n"

	exitCode, ok := ParseExitMarker(stdout, markerKey)
	require.True(t, ok)
	require.Equal(t, int32(7), exitCode)

	clean := StripExitMarker(stdout, markerKey)
	require.Equal(t, "hello\n", clean)
}

func TestBashExitCodeFilter_DoesNotLeakMarkerAcrossChunks(t *testing.T) {
	markerKey := BashExitMarkerPrefix + "token-2"
	full := "hi\n" + markerKey + "=3\n"

	filter := NewBashExitCodeFilter(markerKey)
	chunks := []string{
		full[:5],
		full[5:17],
		full[17 : len(full)-2],
		full[len(full)-2:],
	}

	var streamed strings.Builder
	for _, c := range chunks {
		out := filter.HandleChunk(c)
		require.NotContains(t, out, markerKey)
		streamed.WriteString(out)
	}
	streamed.WriteString(filter.Flush())

	require.Equal(t, "hi\n", streamed.String())
	exitCode, ok := filter.ExitCode()
	require.True(t, ok)
	require.Equal(t, int32(3), exitCode)
}
