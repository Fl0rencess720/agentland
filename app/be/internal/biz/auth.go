package biz

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/autherr"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/jwtc"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/token"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type UserRepo interface {
	UpsertGitHubUser(ctx context.Context, profile *models.GitHubUserProfile) (*models.User, error)
	GetUserByID(ctx context.Context, userID string) (*models.User, error)
}

type AuthRepo interface {
	CreateSession(ctx context.Context, input *models.CreateSessionInput) (*models.AuthSession, error)
	RotateRefreshToken(ctx context.Context, input *models.RotateRefreshTokenInput) (*models.RotateRefreshTokenResult, error)
	RevokeSessionByRefreshToken(ctx context.Context, input *models.RevokeSessionByTokenInput) error
}

type OAuthStateStore interface {
	SaveGitHubState(ctx context.Context, state *models.GitHubOAuthState, ttl time.Duration) error
	ConsumeGitHubState(ctx context.Context, state string) (*models.GitHubOAuthState, error)
}

type GitHubOAuthClient interface {
	ExchangeCode(ctx context.Context, code, redirectURI string) (string, error)
	FetchUser(ctx context.Context, accessToken string) (*models.GitHubUserProfile, error)
	FetchPrimaryVerifiedEmail(ctx context.Context, accessToken string) (string, error)
}

type authConfig struct {
	ClientID             string
	ClientSecret         string
	AuthorizeURL         string
	RedirectURIAllowlist []string
	Scopes               []string
	AccessTTL            time.Duration
	RefreshTTL           time.Duration
	OAuthStateTTL        time.Duration
	JWTIssuer            string
	JWTAudience          string
	JWTPrivateKeyPath    string
	JWTPublicKeyPath     string
	DefaultPlan          string
}

type authUseCase struct {
	userRepo     UserRepo
	authRepo     AuthRepo
	stateStore   OAuthStateStore
	githubClient GitHubOAuthClient
	cfg          authConfig
	now          func() time.Time
	jwtOnce      sync.Once
	jwtManager   *jwtc.Manager
	jwtErr       error
}

func NewAuthUsecase(userRepo UserRepo, authRepo AuthRepo, stateStore OAuthStateStore, githubClient GitHubOAuthClient) AuthUseCase {
	return &authUseCase{
		userRepo:     userRepo,
		authRepo:     authRepo,
		stateStore:   stateStore,
		githubClient: githubClient,
		cfg: authConfig{
			ClientID:             viper.GetString("auth.github.client_id"),
			ClientSecret:         viper.GetString("auth.github.client_secret"),
			AuthorizeURL:         viper.GetString("auth.github.authorize_url"),
			RedirectURIAllowlist: getStringSlice("auth.github.redirect_uri_allowlist"),
			Scopes:               getStringSlice("auth.github.scopes"),
			AccessTTL:            viper.GetDuration("auth.access_ttl"),
			RefreshTTL:           viper.GetDuration("auth.refresh_ttl"),
			OAuthStateTTL:        viper.GetDuration("auth.oauth_state_ttl"),
			JWTIssuer:            viper.GetString("auth.jwt.issuer"),
			JWTAudience:          viper.GetString("auth.jwt.audience"),
			JWTPrivateKeyPath:    viper.GetString("auth.jwt.private_key_path"),
			JWTPublicKeyPath:     viper.GetString("auth.jwt.public_key_path"),
			DefaultPlan:          viper.GetString("auth.user.default_plan"),
		},
		now: time.Now,
	}
}

func (u *authUseCase) GitHubStart(ctx context.Context, req *models.GitHubStartReq) (*models.GitHubStartResp, *response.APIError) {
	redirectURI := strings.TrimSpace(req.RedirectURI)
	if !u.isAllowedRedirectURI(redirectURI) {
		zap.L().Warn("github start redirect uri not allowed", zap.String("redirect_uri", redirectURI))
		return nil, response.InvalidArgumentError("redirect_uri", "not allowed")
	}
	state, err := token.NewOpaque("st_")
	if err != nil {
		zap.L().Error("generate github oauth state failed", zap.Error(err))
		return nil, response.InternalError()
	}
	if err = u.stateStore.SaveGitHubState(ctx, &models.GitHubOAuthState{
		State:       state,
		RedirectURI: redirectURI,
		IssuedAt:    u.now().UTC(),
	}, u.cfg.OAuthStateTTL); err != nil {
		zap.L().Error("save github oauth state failed", zap.Error(err), zap.String("redirect_uri", redirectURI))
		return nil, response.InternalError()
	}

	return &models.GitHubStartResp{
		AuthorizeURL: u.buildAuthorizeURL(redirectURI, state),
		State:        state,
	}, nil
}

func (u *authUseCase) GitHubCallback(ctx context.Context, req *models.GitHubCallbackReq, userAgent, ip string) (*models.GitHubCallbackResp, *response.APIError) {
	stateRecord, err := u.stateStore.ConsumeGitHubState(ctx, strings.TrimSpace(req.State))
	if err != nil {
		zap.L().Warn("consume github oauth state failed", zap.Error(err))
		return nil, u.apiError(err)
	}

	githubAccessToken, err := u.githubClient.ExchangeCode(ctx, strings.TrimSpace(req.Code), stateRecord.RedirectURI)
	if err != nil {
		zap.L().Warn("exchange github code failed", zap.Error(err), zap.String("redirect_uri", stateRecord.RedirectURI))
		return nil, u.apiError(err)
	}

	githubUser, err := u.githubClient.FetchUser(ctx, githubAccessToken)
	if err != nil {
		zap.L().Error("fetch github user failed", zap.Error(err))
		return nil, u.apiError(err)
	}
	if strings.TrimSpace(githubUser.Email) == "" {
		email, emailErr := u.githubClient.FetchPrimaryVerifiedEmail(ctx, githubAccessToken)
		if emailErr != nil {
			zap.L().Error("fetch github primary email failed", zap.Error(emailErr), zap.String("github_user_id", githubUser.ID), zap.String("github_login", githubUser.Login))
			return nil, u.apiError(emailErr)
		}
		githubUser.Email = email
	}

	user, err := u.userRepo.UpsertGitHubUser(ctx, githubUser)
	if err != nil {
		zap.L().Error("upsert github user failed", zap.Error(err), zap.String("github_user_id", githubUser.ID), zap.String("github_login", githubUser.Login), zap.String("email", githubUser.Email))
		return nil, response.InternalError()
	}

	rawRefreshToken, err := token.NewOpaque("rt_")
	if err != nil {
		zap.L().Error("generate refresh token failed", zap.Error(err), zap.String("user_id", user.ID))
		return nil, response.InternalError()
	}
	issuedAt := u.now().UTC()
	sessionID := token.NewID("sess")
	refreshFamilyID := token.NewID("rtf")
	refreshTokenID := token.NewID("rtt")
	session, err := u.authRepo.CreateSession(ctx, &models.CreateSessionInput{
		UserID:           user.ID,
		SessionID:        sessionID,
		RefreshFamilyID:  refreshFamilyID,
		RefreshTokenID:   refreshTokenID,
		RefreshTokenHash: token.Hash(rawRefreshToken),
		UserAgent:        userAgent,
		IP:               ip,
		Now:              issuedAt,
		RefreshExpiresAt: issuedAt.Add(u.cfg.RefreshTTL),
		SessionExpiresAt: issuedAt.Add(u.cfg.RefreshTTL),
	})
	if err != nil {
		zap.L().Error("create auth session failed", zap.Error(err), zap.String("user_id", user.ID), zap.String("session_id", sessionID), zap.String("refresh_family_id", refreshFamilyID))
		return nil, response.InternalError()
	}

	jwtManager, apiErr := u.getJWTManager()
	if apiErr != nil {
		return nil, apiErr
	}
	accessToken, accessExpiresAt, err := jwtManager.SignAccessToken(user.ID, session.ID, issuedAt)
	if err != nil {
		zap.L().Error("sign access token failed", zap.Error(err), zap.String("user_id", user.ID), zap.String("session_id", session.ID))
		return nil, response.InternalError()
	}

	return &models.GitHubCallbackResp{
		User: models.UserProfile{
			ID:          user.ID,
			Email:       user.Email,
			Name:        user.Name,
			AvatarURL:   user.AvatarURL,
			GitHubID:    githubUser.ID,
			GitHubLogin: githubUser.Login,
			Plan:        user.Plan,
		},
		AccessToken:  accessToken,
		RefreshToken: rawRefreshToken,
		ExpiresIn:    int(time.Until(accessExpiresAt).Seconds()),
	}, nil
}

func (u *authUseCase) Refresh(ctx context.Context, req *models.RefreshTokenReq, userAgent, ip string) (*models.RefreshTokenResp, *response.APIError) {
	newRawRefreshToken, err := token.NewOpaque("rt_")
	if err != nil {
		return nil, response.InternalError()
	}
	issuedAt := u.now().UTC()
	result, err := u.authRepo.RotateRefreshToken(ctx, &models.RotateRefreshTokenInput{
		CurrentTokenHash: token.Hash(strings.TrimSpace(req.RefreshToken)),
		NewTokenID:       token.NewID("rtt"),
		NewTokenHash:     token.Hash(newRawRefreshToken),
		Now:              issuedAt,
		NewExpiresAt:     issuedAt.Add(u.cfg.RefreshTTL),
	})
	if err != nil {
		return nil, u.apiError(err)
	}
	_ = userAgent
	_ = ip

	jwtManager, apiErr := u.getJWTManager()
	if apiErr != nil {
		return nil, apiErr
	}
	accessToken, accessExpiresAt, err := jwtManager.SignAccessToken(result.UserID, result.SessionID, issuedAt)
	if err != nil {
		return nil, response.InternalError()
	}

	return &models.RefreshTokenResp{
		AccessToken:  accessToken,
		RefreshToken: newRawRefreshToken,
		ExpiresIn:    int(time.Until(accessExpiresAt).Seconds()),
	}, nil
}

func (u *authUseCase) Me(ctx context.Context, principal models.AuthPrincipal) (*models.CurrentUserResp, *response.APIError) {
	user, err := u.userRepo.GetUserByID(ctx, principal.UserID)
	if err != nil {
		return nil, u.apiError(err)
	}
	return &models.CurrentUserResp{
		ID:        user.ID,
		Email:     user.Email,
		Name:      user.Name,
		AvatarURL: user.AvatarURL,
		Plan:      user.Plan,
	}, nil
}

func (u *authUseCase) Logout(ctx context.Context, principal models.AuthPrincipal, req *models.LogoutReq) (*models.LogoutResp, *response.APIError) {
	err := u.authRepo.RevokeSessionByRefreshToken(ctx, &models.RevokeSessionByTokenInput{
		RefreshTokenHash: token.Hash(strings.TrimSpace(req.RefreshToken)),
		UserID:           principal.UserID,
		SessionID:        principal.SessionID,
		Now:              u.now().UTC(),
	})
	if err != nil {
		return nil, u.apiError(err)
	}
	return &models.LogoutResp{Success: true}, nil
}

func (u *authUseCase) buildAuthorizeURL(redirectURI, state string) string {
	values := url.Values{}
	values.Set("client_id", u.cfg.ClientID)
	values.Set("redirect_uri", redirectURI)
	values.Set("scope", strings.Join(u.cfg.Scopes, " "))
	values.Set("state", state)
	return u.cfg.AuthorizeURL + "?" + values.Encode()
}

func (u *authUseCase) isAllowedRedirectURI(redirectURI string) bool {
	if redirectURI == "" {
		return false
	}
	for _, item := range u.cfg.RedirectURIAllowlist {
		if redirectURI == strings.TrimSpace(item) {
			return true
		}
	}
	return false
}

func (u *authUseCase) getJWTManager() (*jwtc.Manager, *response.APIError) {
	u.jwtOnce.Do(func() {
		u.jwtManager, u.jwtErr = jwtc.NewManager(jwtc.Config{
			PrivateKeyPath: u.cfg.JWTPrivateKeyPath,
			PublicKeyPath:  u.cfg.JWTPublicKeyPath,
			Issuer:         u.cfg.JWTIssuer,
			Audience:       u.cfg.JWTAudience,
			TTL:            u.cfg.AccessTTL,
		})
	})
	if u.jwtErr != nil {
		zap.L().Error("init jwt manager failed", zap.Error(u.jwtErr), zap.String("private_key_path", u.cfg.JWTPrivateKeyPath), zap.String("public_key_path", u.cfg.JWTPublicKeyPath))
		return nil, response.InternalError()
	}
	return u.jwtManager, nil
}

func (u *authUseCase) apiError(err error) *response.APIError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, autherr.ErrRedirectURINotAllow):
		return response.InvalidArgumentError("redirect_uri", "not allowed")
	case errors.Is(err, autherr.ErrOAuthStateNotFound):
		return response.UnauthorizedError()
	case errors.Is(err, autherr.ErrUserNotFound):
		return response.UnauthorizedError()
	case errors.Is(err, autherr.ErrUnauthorized):
		return response.UnauthorizedError()
	case errors.Is(err, autherr.ErrRefreshReplay):
		return response.UnauthorizedError()
	case errors.Is(err, autherr.ErrRefreshExpired):
		return response.UnauthorizedError()
	case errors.Is(err, autherr.ErrSessionRevoked):
		return response.UnauthorizedError()
	default:
		return response.InternalError()
	}
}

func getStringSlice(key string) []string {
	values := viper.GetStringSlice(key)
	if len(values) > 0 {
		return values
	}
	raw := strings.TrimSpace(viper.GetString(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	items := make([]string, 0, len(parts))
	for _, item := range parts {
		trimmed := strings.TrimSpace(item)
		if trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

var _ = fmt.Sprintf
