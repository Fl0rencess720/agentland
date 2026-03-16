package biz

import (
	"context"
	"errors"
	"strings"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/autherr"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
)

type JobRepo interface {
	CreateJob(ctx context.Context, input *models.CreateJobInput) (*models.Job, error)
	GetJobByID(ctx context.Context, ownerID, jobID string) (*models.Job, error)
	GetLatestProjectRuntime(ctx context.Context, ownerID, projectID string) (*models.Job, error)
	UpdateJob(ctx context.Context, input *models.UpdateJobInput) error
}

type jobUseCase struct {
	repo JobRepo
}

func NewJobUsecase(repo JobRepo) JobUseCase {
	return &jobUseCase{repo: repo}
}

func (u *jobUseCase) Detail(ctx context.Context, principal models.AuthPrincipal, jobID string) (*models.JobStatusResp, *response.APIError) {
	if strings.TrimSpace(principal.UserID) == "" {
		return nil, response.UnauthorizedError()
	}
	job, err := u.repo.GetJobByID(ctx, principal.UserID, strings.TrimSpace(jobID))
	if err != nil {
		return nil, u.apiError(err)
	}
	logs := job.Logs
	if logs == nil {
		logs = []string{}
	}
	return &models.JobStatusResp{
		JobID:    job.ID,
		Type:     job.Type,
		Status:   job.Status,
		Progress: job.Progress,
		Logs:     logs,
		Result:   job.Result,
	}, nil
}

func (u *jobUseCase) apiError(err error) *response.APIError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, autherr.ErrJobNotFound):
		return response.NotFoundError()
	default:
		return response.InternalError()
	}
}
