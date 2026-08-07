package biz

import (
	"context"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/autherr"
	securetoken "github.com/Fl0rencess720/agentland/app/be/internal/pkgs/token"
	"github.com/Fl0rencess720/agentland/pkg/common/testutil"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

type fakeUserRepo struct {
	user *models.User
	last *models.GitHubUserProfile
}

func (f *fakeUserRepo) UpsertGitHubUser(_ context.Context, profile *models.GitHubUserProfile) (*models.User, error) {
	f.last = profile
	if f.user == nil {
		f.user = &models.User{ID: "u_123", Email: profile.Email, Name: profile.Name, AvatarURL: profile.AvatarURL, Plan: "free"}
	}
	f.user.Email = profile.Email
	f.user.Name = profile.Name
	return f.user, nil
}

func (f *fakeUserRepo) GetUserByID(_ context.Context, userID string) (*models.User, error) {
	if f.user == nil || f.user.ID != userID {
		return nil, autherr.ErrUserNotFound
	}
	return f.user, nil
}

type fakeAuthRepo struct {
	rotated bool
	revoked bool
}

func (f *fakeAuthRepo) CreateSession(_ context.Context, input *models.CreateSessionInput) (*models.AuthSession, error) {
	return &models.AuthSession{ID: input.SessionID, UserID: input.UserID}, nil
}

func (f *fakeAuthRepo) RotateRefreshToken(_ context.Context, input *models.RotateRefreshTokenInput) (*models.RotateRefreshTokenResult, error) {
	if input.CurrentTokenHash == securetoken.Hash("replay") {
		return nil, autherr.ErrRefreshReplay
	}
	f.rotated = true
	return &models.RotateRefreshTokenResult{UserID: "u_123", SessionID: "sess_123", FamilyID: "rtf_123"}, nil
}

func (f *fakeAuthRepo) RevokeSessionByRefreshToken(_ context.Context, _ *models.RevokeSessionByTokenInput) error {
	f.revoked = true
	return nil
}

type fakeStateStore struct {
	stored map[string]*models.GitHubOAuthState
}

func (f *fakeStateStore) SaveGitHubState(_ context.Context, state *models.GitHubOAuthState, _ time.Duration) error {
	if f.stored == nil {
		f.stored = map[string]*models.GitHubOAuthState{}
	}
	f.stored[state.State] = state
	return nil
}

func (f *fakeStateStore) ConsumeGitHubState(_ context.Context, state string) (*models.GitHubOAuthState, error) {
	item, ok := f.stored[state]
	if !ok {
		return nil, autherr.ErrOAuthStateNotFound
	}
	delete(f.stored, state)
	return item, nil
}

type fakeGitHubClient struct{}

func (f *fakeGitHubClient) ExchangeCode(_ context.Context, code, redirectURI string) (string, error) {
	if code == "bad" || redirectURI == "" {
		return "", autherr.ErrUnauthorized
	}
	return "gh_token", nil
}

func (f *fakeGitHubClient) FetchUser(_ context.Context, _ string) (*models.GitHubUserProfile, error) {
	return &models.GitHubUserProfile{ID: "123456", Login: "alice-dev", Name: "Alice", AvatarURL: "https://avatar.example.com/1.png"}, nil
}

func (f *fakeGitHubClient) FetchPrimaryVerifiedEmail(_ context.Context, _ string) (string, error) {
	return "user@company.com", nil
}

func TestAuthUseCaseGitHubStartAndCallback(t *testing.T) {
	privatePath, publicPath, err := testutil.WriteTestRSAKeys(t.TempDir())
	require.NoError(t, err)
	viper.Set("auth.github.client_id", "client_123")
	viper.Set("auth.github.redirect_uri_allowlist", []string{"https://app.example.com/auth/github/callback"})
	viper.Set("auth.github.scopes", []string{"read:user", "user:email"})
	viper.Set("auth.github.authorize_url", "https://github.com/login/oauth/authorize")
	viper.Set("auth.oauth_state_ttl", 10*time.Minute)
	viper.Set("auth.access_ttl", 15*time.Minute)
	viper.Set("auth.refresh_ttl", 30*24*time.Hour)
	viper.Set("auth.jwt.issuer", "agentland-app-be")
	viper.Set("auth.jwt.audience", "agentland-app")
	viper.Set("auth.jwt.private_key_path", privatePath)
	viper.Set("auth.jwt.public_key_path", publicPath)
	viper.Set("auth.user.default_plan", "free")

	stateStore := &fakeStateStore{}
	userRepo := &fakeUserRepo{}
	authRepo := &fakeAuthRepo{}
	useCase := NewAuthUsecase(userRepo, authRepo, stateStore, &fakeGitHubClient{})

	startResp, apiErr := useCase.GitHubStart(context.Background(), &models.GitHubStartReq{RedirectURI: "https://app.example.com/auth/github/callback"})
	require.Nil(t, apiErr)
	require.NotEmpty(t, startResp.State)
	require.Contains(t, startResp.AuthorizeURL, "client_id=client_123")

	callbackResp, apiErr := useCase.GitHubCallback(context.Background(), &models.GitHubCallbackReq{Code: "ok", State: startResp.State, Nonce: startResp.Nonce}, "ua", "127.0.0.1")
	require.Nil(t, apiErr)
	require.Equal(t, "user@company.com", callbackResp.User.Email)
	require.NotEmpty(t, callbackResp.AccessToken)
	require.NotEmpty(t, callbackResp.RefreshToken)
}

func TestAuthUseCaseRejectsOAuthNonceMismatch(t *testing.T) {
	viper.Set("auth.github.redirect_uri_allowlist", []string{"https://app.example.com/auth/github/callback"})
	viper.Set("auth.github.authorize_url", "https://github.com/login/oauth/authorize")
	viper.Set("auth.oauth_state_ttl", 10*time.Minute)
	stateStore := &fakeStateStore{}
	useCase := NewAuthUsecase(&fakeUserRepo{}, &fakeAuthRepo{}, stateStore, &fakeGitHubClient{})

	startResp, apiErr := useCase.GitHubStart(context.Background(), &models.GitHubStartReq{RedirectURI: "https://app.example.com/auth/github/callback"})
	require.Nil(t, apiErr)
	_, apiErr = useCase.GitHubCallback(context.Background(), &models.GitHubCallbackReq{Code: "ok", State: startResp.State, Nonce: "wrong"}, "ua", "127.0.0.1")
	require.NotNil(t, apiErr)
	require.Equal(t, 401, apiErr.StatusCode)
	_, exists := stateStore.stored[startResp.State]
	require.False(t, exists)
}

func TestAuthUseCaseRefreshReplay(t *testing.T) {
	privatePath, publicPath, err := testutil.WriteTestRSAKeys(t.TempDir())
	require.NoError(t, err)
	viper.Set("auth.access_ttl", 15*time.Minute)
	viper.Set("auth.refresh_ttl", 30*24*time.Hour)
	viper.Set("auth.jwt.issuer", "agentland-app-be")
	viper.Set("auth.jwt.audience", "agentland-app")
	viper.Set("auth.jwt.private_key_path", privatePath)
	viper.Set("auth.jwt.public_key_path", publicPath)

	useCase := NewAuthUsecase(&fakeUserRepo{user: &models.User{ID: "u_123"}}, &fakeAuthRepo{}, &fakeStateStore{}, &fakeGitHubClient{})
	_, apiErr := useCase.Refresh(context.Background(), &models.RefreshTokenReq{RefreshToken: "replay"}, "ua", "127.0.0.1")
	require.NotNil(t, apiErr)
	require.Equal(t, 401, apiErr.StatusCode)
}
