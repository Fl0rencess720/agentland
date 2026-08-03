package biz

import (
	"context"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
)

type AuthUseCase interface {
	GitHubStart(context.Context, *models.GitHubStartReq) (*models.GitHubStartResp, *response.APIError)
	GitHubCallback(context.Context, *models.GitHubCallbackReq, string, string) (*models.GitHubCallbackResp, *response.APIError)
	Refresh(context.Context, *models.RefreshTokenReq, string, string) (*models.RefreshTokenResp, *response.APIError)
	Me(context.Context, models.AuthPrincipal) (*models.CurrentUserResp, *response.APIError)
	Logout(context.Context, models.AuthPrincipal, *models.LogoutReq) (*models.LogoutResp, *response.APIError)
}

type ProjectUseCase interface {
	List(context.Context, models.AuthPrincipal, *models.ProjectListReq) (*models.ProjectListResp, *response.APIError)
	Create(context.Context, models.AuthPrincipal, *models.ProjectCreateReq) (*models.ProjectCreateResp, *response.APIError)
	Detail(context.Context, models.AuthPrincipal, string) (*models.ProjectDetailResp, *response.APIError)
	Update(context.Context, models.AuthPrincipal, string, *models.ProjectUpdateReq) (*models.ProjectUpdateResp, *response.APIError)
	Delete(context.Context, models.AuthPrincipal, string) (*models.ProjectDeleteResp, *response.APIError)
	Usage(context.Context, models.AuthPrincipal) (*models.ProjectUsageResp, *response.APIError)
	CreateRun(context.Context, models.AuthPrincipal, string, string, *models.RunCreateReq) (*models.RunCreateResp, *response.APIError)
	GetRun(context.Context, models.AuthPrincipal, string) (*models.RunResp, *response.APIError)
	StreamRunEvents(context.Context, models.AuthPrincipal, string, string, func(*models.StoredRunEvent) error) *response.APIError
	CancelRun(context.Context, models.AuthPrincipal, string) (*models.RunCancelResp, *response.APIError)
	ListMessages(context.Context, models.AuthPrincipal, string, *models.MessageListReq) (*models.MessageListResp, *response.APIError)
	FileTree(context.Context, models.AuthPrincipal, string, *models.FileTreeReq) (*models.FileTreeResp, *response.APIError)
	FileContent(context.Context, models.AuthPrincipal, string, *models.FileContentReq) (*models.FileContentResp, *response.APIError)
	UpdateFileContent(context.Context, models.AuthPrincipal, string, *models.FileContentReq, *models.FileContentUpdateReq) (*models.FileContentUpdateResp, *response.APIError)
	StartPreview(context.Context, models.AuthPrincipal, string, *models.PreviewStartReq) (*models.PreviewResp, *response.APIError)
	Preview(context.Context, models.AuthPrincipal, string) (*models.PreviewResp, *response.APIError)
}
