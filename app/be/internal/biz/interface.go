package biz

import (
	"context"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
)

type AuthUseCase interface {
	GitHubStart(ctx context.Context, req *models.GitHubStartReq) (*models.GitHubStartResp, *response.APIError)
	GitHubCallback(ctx context.Context, req *models.GitHubCallbackReq, userAgent, ip string) (*models.GitHubCallbackResp, *response.APIError)
	Refresh(ctx context.Context, req *models.RefreshTokenReq, userAgent, ip string) (*models.RefreshTokenResp, *response.APIError)
	Me(ctx context.Context, principal models.AuthPrincipal) (*models.CurrentUserResp, *response.APIError)
	Logout(ctx context.Context, principal models.AuthPrincipal, req *models.LogoutReq) (*models.LogoutResp, *response.APIError)
}

type ProjectUseCase interface {
	List(ctx context.Context, principal models.AuthPrincipal, req *models.ProjectListReq) (*models.ProjectListResp, *response.APIError)
	Create(ctx context.Context, principal models.AuthPrincipal, req *models.ProjectCreateReq) (*models.ProjectCreateResp, *response.APIError)
	Detail(ctx context.Context, principal models.AuthPrincipal, projectID string) (*models.ProjectDetailResp, *response.APIError)
	Update(ctx context.Context, principal models.AuthPrincipal, projectID string, req *models.ProjectUpdateReq) (*models.ProjectUpdateResp, *response.APIError)
	Delete(ctx context.Context, principal models.AuthPrincipal, projectID string) (*models.ProjectDeleteResp, *response.APIError)
	Usage(ctx context.Context, principal models.AuthPrincipal) (*models.ProjectUsageResp, *response.APIError)
}

type UserUseCase interface{}
type JobUseCase interface{}
type DeploymentUseCase interface{}
type FileUseCase interface{}
