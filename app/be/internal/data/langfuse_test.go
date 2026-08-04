package data

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/stretchr/testify/require"
)

func TestLangfuseScoreClientPublishesReplayScores(t *testing.T) {
	var scores []map[string]any
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, "/api/public/scores", request.URL.Path)
		public, secret, ok := request.BasicAuth()
		require.True(t, ok)
		require.Equal(t, "pk-test", public)
		require.Equal(t, "sk-test", secret)
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		var score map[string]any
		require.NoError(t, json.Unmarshal(body, &score))
		scores = append(scores, score)
		return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"score"}`)), Request: request}, nil
	})
	client := &langfuseScoreClient{
		enabled: true, baseURL: "https://langfuse.example", public: "pk-test", secret: "sk-test",
		httpClient: &http.Client{Transport: transport},
	}
	err := client.ScoreReplay(context.Background(), "trace-1", &models.ReplayRunResp{
		ID: "replay-1", SourceRunID: "run-1", Mode: models.ReplayModeLive, Status: "completed", Score: 0.75,
	})
	require.NoError(t, err)
	require.Len(t, scores, 2)
	require.Equal(t, "agent-trajectory-match", scores[0]["name"])
	require.Equal(t, 0.75, scores[0]["value"])
	require.Equal(t, "agent-task-completed", scores[1]["name"])
	require.Equal(t, float64(1), scores[1]["value"])
}

func TestLangfuseScoreClientIsNoopWhenDisabled(t *testing.T) {
	client := &langfuseScoreClient{}
	require.NoError(t, client.ScoreReplay(context.Background(), "", nil))
}
