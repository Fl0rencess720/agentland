package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/autherr"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/token"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
)

const githubProvider = "github"

var (
	sharedAuthStoreOnce sync.Once
	sharedAuthStore     *authStore
)

type authStore struct {
	poolOnce    sync.Once
	pool        *pgxpool.Pool
	poolErr     error
	redisOnce   sync.Once
	redis       *redis.Client
	redisErr    error
	schemaMu    sync.Mutex
	schemaReady bool
	schemaErr   error
	github      *gitHubOAuthClient
}

type gitHubOAuthClient struct {
	httpClient   *http.Client
	clientID     string
	clientSecret string
	authorizeURL string
	tokenURL     string
	apiBaseURL   string
}

type githubAccessTokenResp struct {
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

type githubUserAPIResp struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
}

type githubEmailAPIResp struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func NewAuthRepo() biz.AuthRepo {
	return sharedStore()
}

func NewUserRepo() biz.UserRepo {
	return sharedStore()
}

func NewOAuthStateStore() biz.OAuthStateStore {
	return sharedStore()
}

func NewGitHubOAuthClient() biz.GitHubOAuthClient {
	return sharedStore().github
}

func sharedStore() *authStore {
	sharedAuthStoreOnce.Do(func() {
		sharedAuthStore = &authStore{
			github: &gitHubOAuthClient{
				httpClient:   &http.Client{Timeout: 10 * time.Second},
				clientID:     viper.GetString("auth.github.client_id"),
				clientSecret: viper.GetString("auth.github.client_secret"),
				authorizeURL: viper.GetString("auth.github.authorize_url"),
				tokenURL:     viper.GetString("auth.github.token_url"),
				apiBaseURL:   strings.TrimRight(viper.GetString("auth.github.api_base_url"), "/"),
			},
		}
	})
	return sharedAuthStore
}

func (s *authStore) UpsertGitHubUser(ctx context.Context, profile *models.GitHubUserProfile) (*models.User, error) {
	pool, err := s.ensurePool(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.ensureSchema(ctx); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	now := time.Now().UTC()
	user, err := s.findUserByProvider(ctx, tx, profile.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		user, err = s.findOrCreateUserByEmail(ctx, tx, profile, now)
		if err != nil {
			return nil, err
		}
	}

	user, err = s.updateUserProfile(ctx, tx, user.ID, profile, now)
	if err != nil {
		return nil, err
	}
	if err = s.upsertIdentity(ctx, tx, user.ID, profile, now); err != nil {
		return nil, err
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return user, nil
}

func (s *authStore) GetUserByID(ctx context.Context, userID string) (*models.User, error) {
	pool, err := s.ensurePool(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	query := `select id, coalesce(email,''), name, avatar_url, plan, status, coalesce(last_login_at, now()), created_at, updated_at from users where id = $1`
	user := &models.User{}
	if err = pool.QueryRow(ctx, query, userID).Scan(&user.ID, &user.Email, &user.Name, &user.AvatarURL, &user.Plan, &user.Status, &user.LastLoginAt, &user.CreatedAt, &user.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, autherr.ErrUserNotFound
		}
		return nil, err
	}
	return user, nil
}

func (s *authStore) CreateSession(ctx context.Context, input *models.CreateSessionInput) (*models.AuthSession, error) {
	pool, err := s.ensurePool(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	sessionQuery := `insert into auth_sessions (id, user_id, status, refresh_family_id, current_refresh_token_id, user_agent, ip, last_seen_at, expires_at, revoked_at, created_at)
values ($1,$2,'active',$3,$4,$5,$6,$7,$8,null,$7)`
	if _, err = tx.Exec(ctx, sessionQuery, input.SessionID, input.UserID, input.RefreshFamilyID, input.RefreshTokenID, input.UserAgent, input.IP, input.Now, input.SessionExpiresAt); err != nil {
		return nil, err
	}
	tokenQuery := `insert into auth_refresh_tokens (id, session_id, family_id, token_hash, parent_token_id, replaced_by_token_id, issued_at, expires_at, consumed_at, revoked_at)
values ($1,$2,$3,$4,null,null,$5,$6,null,null)`
	if _, err = tx.Exec(ctx, tokenQuery, input.RefreshTokenID, input.SessionID, input.RefreshFamilyID, input.RefreshTokenHash, input.Now, input.RefreshExpiresAt); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &models.AuthSession{
		ID:                    input.SessionID,
		UserID:                input.UserID,
		Status:                "active",
		RefreshFamilyID:       input.RefreshFamilyID,
		CurrentRefreshTokenID: input.RefreshTokenID,
		UserAgent:             input.UserAgent,
		IP:                    input.IP,
		LastSeenAt:            input.Now,
		ExpiresAt:             input.SessionExpiresAt,
		CreatedAt:             input.Now,
	}, nil
}

func (s *authStore) RotateRefreshToken(ctx context.Context, input *models.RotateRefreshTokenInput) (*models.RotateRefreshTokenResult, error) {
	pool, err := s.ensurePool(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	query := `select t.id, t.session_id, t.family_id, t.expires_at, t.consumed_at, t.revoked_at, s.user_id, s.status, s.revoked_at
from auth_refresh_tokens t
join auth_sessions s on s.id = t.session_id
where t.token_hash = $1
for update`
	var tokenID, sessionID, familyID, userID, sessionStatus string
	var expiresAt time.Time
	var consumedAt, revokedAt, sessionRevokedAt *time.Time
	if err = tx.QueryRow(ctx, query, input.CurrentTokenHash).Scan(&tokenID, &sessionID, &familyID, &expiresAt, &consumedAt, &revokedAt, &userID, &sessionStatus, &sessionRevokedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, autherr.ErrUnauthorized
		}
		return nil, err
	}
	if revokedAt != nil || sessionRevokedAt != nil || sessionStatus != "active" {
		return nil, autherr.ErrSessionRevoked
	}
	if input.Now.After(expiresAt) {
		return nil, autherr.ErrRefreshExpired
	}
	if consumedAt != nil {
		if err = s.revokeFamilyTx(ctx, tx, familyID, sessionID, input.Now); err != nil {
			return nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, autherr.ErrRefreshReplay
	}
	updateOld := `update auth_refresh_tokens set consumed_at = $2, replaced_by_token_id = $3 where id = $1`
	if _, err = tx.Exec(ctx, updateOld, tokenID, input.Now, input.NewTokenID); err != nil {
		return nil, err
	}
	insertNew := `insert into auth_refresh_tokens (id, session_id, family_id, token_hash, parent_token_id, replaced_by_token_id, issued_at, expires_at, consumed_at, revoked_at)
values ($1,$2,$3,$4,$5,null,$6,$7,null,null)`
	if _, err = tx.Exec(ctx, insertNew, input.NewTokenID, sessionID, familyID, input.NewTokenHash, tokenID, input.Now, input.NewExpiresAt); err != nil {
		return nil, err
	}
	updateSession := `update auth_sessions set current_refresh_token_id = $2, last_seen_at = $3 where id = $1`
	if _, err = tx.Exec(ctx, updateSession, sessionID, input.NewTokenID, input.Now); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &models.RotateRefreshTokenResult{UserID: userID, SessionID: sessionID, FamilyID: familyID}, nil
}

func (s *authStore) RevokeSessionByRefreshToken(ctx context.Context, input *models.RevokeSessionByTokenInput) error {
	pool, err := s.ensurePool(ctx)
	if err != nil {
		return err
	}
	if err = s.ensureSchema(ctx); err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	query := `select t.session_id, t.family_id, s.user_id from auth_refresh_tokens t join auth_sessions s on s.id = t.session_id where t.token_hash = $1 for update`
	var sessionID, familyID, userID string
	if err = tx.QueryRow(ctx, query, input.RefreshTokenHash).Scan(&sessionID, &familyID, &userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return autherr.ErrUnauthorized
		}
		return err
	}
	if sessionID != input.SessionID || userID != input.UserID {
		return autherr.ErrUnauthorized
	}
	if err = s.revokeFamilyTx(ctx, tx, familyID, sessionID, input.Now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *authStore) SaveGitHubState(ctx context.Context, state *models.GitHubOAuthState, ttl time.Duration) error {
	client, err := s.ensureRedis()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return client.Set(ctx, githubStateKey(state.State), payload, ttl).Err()
}

func (s *authStore) ConsumeGitHubState(ctx context.Context, state string) (*models.GitHubOAuthState, error) {
	client, err := s.ensureRedis()
	if err != nil {
		return nil, err
	}
	payload, err := client.GetDel(ctx, githubStateKey(state)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, autherr.ErrOAuthStateNotFound
		}
		return nil, err
	}
	var stored models.GitHubOAuthState
	if err = json.Unmarshal([]byte(payload), &stored); err != nil {
		return nil, err
	}
	return &stored, nil
}

func (c *gitHubOAuthClient) ExchangeCode(ctx context.Context, code, redirectURI string) (string, error) {
	values := url.Values{}
	values.Set("client_id", c.clientID)
	values.Set("client_secret", c.clientSecret)
	values.Set("code", code)
	values.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", autherr.ErrUnauthorized
	}
	var tokenResp githubAccessTokenResp
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return "", err
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return "", autherr.ErrUnauthorized
	}
	return tokenResp.AccessToken, nil
}

func (c *gitHubOAuthClient) FetchUser(ctx context.Context, accessToken string) (*models.GitHubUserProfile, error) {
	body, err := c.doGitHubJSON(ctx, http.MethodGet, c.apiBaseURL+"/user", accessToken, nil)
	if err != nil {
		return nil, err
	}
	var apiResp githubUserAPIResp
	if err = json.Unmarshal(body, &apiResp); err != nil {
		return nil, err
	}
	return &models.GitHubUserProfile{
		ID:         fmt.Sprintf("%d", apiResp.ID),
		Login:      apiResp.Login,
		Name:       fallbackName(apiResp.Name, apiResp.Login),
		Email:      strings.ToLower(strings.TrimSpace(apiResp.Email)),
		AvatarURL:  apiResp.AvatarURL,
		RawProfile: append([]byte(nil), body...),
	}, nil
}

func (c *gitHubOAuthClient) FetchPrimaryVerifiedEmail(ctx context.Context, accessToken string) (string, error) {
	body, err := c.doGitHubJSON(ctx, http.MethodGet, c.apiBaseURL+"/user/emails", accessToken, nil)
	if err != nil {
		return "", err
	}
	var emails []githubEmailAPIResp
	if err = json.Unmarshal(body, &emails); err != nil {
		return "", err
	}
	for _, email := range emails {
		if email.Primary && email.Verified && strings.TrimSpace(email.Email) != "" {
			return strings.ToLower(strings.TrimSpace(email.Email)), nil
		}
	}
	for _, email := range emails {
		if email.Verified && strings.TrimSpace(email.Email) != "" {
			return strings.ToLower(strings.TrimSpace(email.Email)), nil
		}
	}
	return "", nil
}

func (c *gitHubOAuthClient) doGitHubJSON(ctx context.Context, method, endpoint, accessToken string, payload []byte) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, autherr.ErrUnauthorized
	}
	return responseBody, nil
}

func (s *authStore) ensurePool(ctx context.Context) (*pgxpool.Pool, error) {
	s.poolOnce.Do(func() {
		dsn := strings.TrimSpace(viper.GetString("database.url"))
		if dsn == "" {
			s.poolErr = fmt.Errorf("database.url is required")
			return
		}
		s.pool, s.poolErr = pgxpool.New(ctx, dsn)
	})
	return s.pool, s.poolErr
}

func (s *authStore) ensureRedis() (*redis.Client, error) {
	s.redisOnce.Do(func() {
		s.redis = redis.NewClient(&redis.Options{
			Addr:         viper.GetString("redis.addr"),
			Password:     viper.GetString("redis.password"),
			DB:           viper.GetInt("redis.db"),
			DialTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			ReadTimeout:  5 * time.Second,
		})
	})
	return s.redis, s.redisErr
}

func (s *authStore) ensureSchema(ctx context.Context) error {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if s.schemaReady {
		return nil
	}
	s.schemaErr = nil
	pool, err := s.ensurePool(ctx)
	if err != nil {
		return err
	}
	{
		statements := []string{
			`create table if not exists users (
				id text primary key,
				email text,
				name text not null,
				avatar_url text not null default '',
				plan text not null default 'free',
				status text not null default 'active',
				last_login_at timestamptz,
				created_at timestamptz not null,
				updated_at timestamptz not null
			)`,
			`create unique index if not exists idx_users_email_lower on users ((lower(email))) where email is not null and email <> ''`,
			`create table if not exists user_identities (
				id text primary key,
				user_id text not null references users(id),
				provider text not null,
				provider_user_id text not null,
				provider_login text not null,
				provider_email text,
				profile_json jsonb not null default '{}'::jsonb,
				linked_at timestamptz not null,
				last_login_at timestamptz not null
			)`,
			`create unique index if not exists idx_user_identities_provider_uid on user_identities(provider, provider_user_id)`,
			`create table if not exists auth_sessions (
				id text primary key,
				user_id text not null references users(id),
				status text not null,
				refresh_family_id text not null,
				current_refresh_token_id text,
				user_agent text,
				ip text,
				last_seen_at timestamptz not null,
				expires_at timestamptz not null,
				revoked_at timestamptz,
				created_at timestamptz not null
			)`,
			`create table if not exists auth_refresh_tokens (
				id text primary key,
				session_id text not null references auth_sessions(id),
				family_id text not null,
				token_hash text not null,
				parent_token_id text,
				replaced_by_token_id text,
				issued_at timestamptz not null,
				expires_at timestamptz not null,
				consumed_at timestamptz,
				revoked_at timestamptz
			)`,
			`create unique index if not exists idx_auth_refresh_tokens_hash on auth_refresh_tokens(token_hash)`,
			`create index if not exists idx_auth_refresh_tokens_family on auth_refresh_tokens(family_id)`,
		}
		for _, stmt := range statements {
			if _, err = pool.Exec(ctx, stmt); err != nil {
				s.schemaErr = err
				break
			}
		}
	}
	if s.schemaErr == nil {
		s.schemaReady = true
	}
	return s.schemaErr
}

func (s *authStore) findUserByProvider(ctx context.Context, tx pgx.Tx, providerUserID string) (*models.User, error) {
	query := `select u.id, coalesce(u.email,''), u.name, u.avatar_url, u.plan, u.status, coalesce(u.last_login_at, now()), u.created_at, u.updated_at
from user_identities ui
join users u on u.id = ui.user_id
where ui.provider = $1 and ui.provider_user_id = $2`
	user := &models.User{}
	err := tx.QueryRow(ctx, query, githubProvider, providerUserID).Scan(&user.ID, &user.Email, &user.Name, &user.AvatarURL, &user.Plan, &user.Status, &user.LastLoginAt, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (s *authStore) findOrCreateUserByEmail(ctx context.Context, tx pgx.Tx, profile *models.GitHubUserProfile, now time.Time) (*models.User, error) {
	if strings.TrimSpace(profile.Email) != "" {
		query := `select id, coalesce(email,''), name, avatar_url, plan, status, coalesce(last_login_at, now()), created_at, updated_at from users where lower(email) = lower($1)`
		user := &models.User{}
		err := tx.QueryRow(ctx, query, profile.Email).Scan(&user.ID, &user.Email, &user.Name, &user.AvatarURL, &user.Plan, &user.Status, &user.LastLoginAt, &user.CreatedAt, &user.UpdatedAt)
		if err == nil {
			return user, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	userID := token.NewID("u")
	insert := `insert into users (id, email, name, avatar_url, plan, status, last_login_at, created_at, updated_at)
values ($1,$2,$3,$4,$5,'active',$6,$6,$6)`
	if _, err := tx.Exec(ctx, insert, userID, nullableString(profile.Email), fallbackName(profile.Name, profile.Login), profile.AvatarURL, viper.GetString("auth.user.default_plan"), now); err != nil {
		return nil, err
	}
	return &models.User{ID: userID, Email: profile.Email, Name: fallbackName(profile.Name, profile.Login), AvatarURL: profile.AvatarURL, Plan: viper.GetString("auth.user.default_plan"), Status: "active", LastLoginAt: now, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *authStore) updateUserProfile(ctx context.Context, tx pgx.Tx, userID string, profile *models.GitHubUserProfile, now time.Time) (*models.User, error) {
	query := `update users set email = $2, name = $3, avatar_url = $4, last_login_at = $5, updated_at = $5 where id = $1
returning id, coalesce(email,''), name, avatar_url, plan, status, coalesce(last_login_at, $5), created_at, updated_at`
	user := &models.User{}
	if err := tx.QueryRow(ctx, query, userID, nullableString(profile.Email), fallbackName(profile.Name, profile.Login), profile.AvatarURL, now).Scan(&user.ID, &user.Email, &user.Name, &user.AvatarURL, &user.Plan, &user.Status, &user.LastLoginAt, &user.CreatedAt, &user.UpdatedAt); err != nil {
		return nil, err
	}
	return user, nil
}

func (s *authStore) upsertIdentity(ctx context.Context, tx pgx.Tx, userID string, profile *models.GitHubUserProfile, now time.Time) error {
	query := `insert into user_identities (id, user_id, provider, provider_user_id, provider_login, provider_email, profile_json, linked_at, last_login_at)
values ($1,$2,$3,$4,$5,$6,$7,$8,$8)
on conflict (provider, provider_user_id) do update set user_id = excluded.user_id, provider_login = excluded.provider_login, provider_email = excluded.provider_email, profile_json = excluded.profile_json, last_login_at = excluded.last_login_at`
	_, err := tx.Exec(ctx, query, token.NewID("ident"), userID, githubProvider, profile.ID, profile.Login, nullableString(profile.Email), profile.RawProfile, now)
	return err
}

func (s *authStore) revokeFamilyTx(ctx context.Context, tx pgx.Tx, familyID, sessionID string, now time.Time) error {
	if _, err := tx.Exec(ctx, `update auth_refresh_tokens set revoked_at = $2 where family_id = $1 and revoked_at is null`, familyID, now); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `update auth_sessions set status = 'revoked', revoked_at = $2 where id = $1 and revoked_at is null`, sessionID, now)
	return err
}

func githubStateKey(state string) string {
	return "auth:github:state:" + state
}

func nullableString(value string) any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return strings.ToLower(trimmed)
}

func fallbackName(name, login string) string {
	if strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	if strings.TrimSpace(login) != "" {
		return strings.TrimSpace(login)
	}
	return "GitHub User"
}
