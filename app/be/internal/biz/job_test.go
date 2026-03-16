package biz

import (
	"context"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/stretchr/testify/require"
)

func TestJobUseCaseDetail(t *testing.T) {
	repo := &fakeJobRepo{jobs: map[string]*models.Job{
		"job_1": {
			ID:        "job_1",
			OwnerID:   "u_123",
			ProjectID: "p_1",
			Type:      "APP_GENERATION",
			Status:    "RUNNING",
			Progress:  42,
			Logs:      []string{"Sandbox ready"},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}}
	useCase := NewJobUsecase(repo)

	resp, apiErr := useCase.Detail(context.Background(), models.AuthPrincipal{UserID: "u_123"}, "job_1")
	require.Nil(t, apiErr)
	require.Equal(t, "job_1", resp.JobID)
	require.Equal(t, "APP_GENERATION", resp.Type)
	require.Equal(t, 42, resp.Progress)
}
