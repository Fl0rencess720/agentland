package models

import (
	"encoding/json"
	"time"
)

type UserProfile struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	AvatarURL   string `json:"avatar_url"`
	GitHubID    string `json:"github_id,omitempty"`
	GitHubLogin string `json:"github_login,omitempty"`
	Plan        string `json:"plan,omitempty"`
}

type GitHubStartReq struct {
	RedirectURI string `json:"redirect_uri" binding:"required"`
}

type GitHubStartResp struct {
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
}

type GitHubCallbackReq struct {
	Code  string `json:"code" binding:"required"`
	State string `json:"state" binding:"required"`
}

type GitHubCallbackResp struct {
	User         UserProfile `json:"user"`
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	ExpiresIn    int         `json:"expires_in"`
}

type RefreshTokenReq struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

type RefreshTokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

type CurrentUserResp struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	Plan      string `json:"plan"`
}

type LogoutReq struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

type LogoutResp struct {
	Success bool `json:"success"`
}

type AuthPrincipal struct {
	UserID    string
	SessionID string
}

type User struct {
	ID          string
	Email       string
	Name        string
	AvatarURL   string
	Plan        string
	Status      string
	LastLoginAt time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type UserIdentity struct {
	ID             string
	UserID         string
	Provider       string
	ProviderUserID string
	ProviderLogin  string
	ProviderEmail  string
	ProfileJSON    json.RawMessage
	LinkedAt       time.Time
	LastLoginAt    time.Time
}

type AuthSession struct {
	ID                    string
	UserID                string
	Status                string
	RefreshFamilyID       string
	CurrentRefreshTokenID string
	UserAgent             string
	IP                    string
	LastSeenAt            time.Time
	ExpiresAt             time.Time
	RevokedAt             *time.Time
	CreatedAt             time.Time
}

type AuthRefreshToken struct {
	ID                string
	SessionID         string
	FamilyID          string
	TokenHash         string
	ParentTokenID     *string
	ReplacedByTokenID *string
	IssuedAt          time.Time
	ExpiresAt         time.Time
	ConsumedAt        *time.Time
	RevokedAt         *time.Time
}

type GitHubOAuthState struct {
	State       string    `json:"state"`
	RedirectURI string    `json:"redirect_uri"`
	IssuedAt    time.Time `json:"issued_at"`
}

type GitHubUserProfile struct {
	ID         string          `json:"id"`
	Login      string          `json:"login"`
	Name       string          `json:"name"`
	Email      string          `json:"email"`
	AvatarURL  string          `json:"avatar_url"`
	RawProfile json.RawMessage `json:"-"`
}

type CreateSessionInput struct {
	UserID           string
	SessionID        string
	RefreshFamilyID  string
	RefreshTokenID   string
	RefreshTokenHash string
	UserAgent        string
	IP               string
	Now              time.Time
	RefreshExpiresAt time.Time
	SessionExpiresAt time.Time
}

type RotateRefreshTokenInput struct {
	CurrentTokenHash string
	NewTokenID       string
	NewTokenHash     string
	Now              time.Time
	NewExpiresAt     time.Time
}

type RotateRefreshTokenResult struct {
	UserID    string
	SessionID string
	FamilyID  string
}

type RevokeSessionByTokenInput struct {
	RefreshTokenHash string
	UserID           string
	SessionID        string
	Now              time.Time
}
