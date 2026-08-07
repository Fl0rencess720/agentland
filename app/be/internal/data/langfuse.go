package data

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/spf13/viper"
)

type langfuseScoreClient struct {
	enabled                 bool
	baseURL, public, secret string
	httpClient              *http.Client
}

func NewLangfuseScoreClient() biz.EvaluationSink {
	return &langfuseScoreClient{
		enabled:    viper.GetBool("langfuse.enabled"),
		baseURL:    strings.TrimRight(strings.TrimSpace(viper.GetString("langfuse.base_url")), "/"),
		public:     strings.TrimSpace(viper.GetString("langfuse.public_key")),
		secret:     strings.TrimSpace(viper.GetString("langfuse.secret_key")),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *langfuseScoreClient) ScoreReplay(ctx context.Context, traceID string, report *models.ReplayRunResp) error {
	if !c.enabled {
		return nil
	}
	if c.baseURL == "" || c.public == "" || c.secret == "" {
		return fmt.Errorf("langfuse base URL and API keys are required")
	}
	if strings.TrimSpace(traceID) == "" || report == nil {
		return fmt.Errorf("langfuse replay score requires trace and report")
	}
	metadata := map[string]string{"replay_id": report.ID, "source_run_id": report.SourceRunID, "mode": report.Mode}
	scores := []map[string]any{
		{
			"id": report.ID + "-trajectory-match", "traceId": traceID, "name": "agent-trajectory-match",
			"value": report.Score, "dataType": "NUMERIC", "comment": "Tool selection and argument match across replayed model steps", "metadata": metadata,
		},
		{
			"id": report.ID + "-task-completed", "traceId": traceID, "name": "agent-task-completed",
			"value": boolScore(report.Status == "completed"), "dataType": "BOOLEAN", "comment": "Replay reached a completed terminal state", "metadata": metadata,
		},
	}
	for _, score := range scores {
		if err := c.createScore(ctx, score); err != nil {
			return err
		}
	}
	return nil
}

func boolScore(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (c *langfuseScoreClient) createScore(ctx context.Context, score map[string]any) error {
	body, err := json.Marshal(score)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/public/scores", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth(c.public, c.secret)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("create Langfuse score: %w", err)
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if readErr != nil {
		return fmt.Errorf("read Langfuse score response: %w", readErr)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("create Langfuse score: status %d: %s", response.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	return nil
}
