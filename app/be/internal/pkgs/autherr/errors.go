package autherr

import "errors"

var (
	ErrUnauthorized        = errors.New("unauthorized")
	ErrRedirectURINotAllow = errors.New("redirect uri not allowed")
	ErrOAuthStateNotFound  = errors.New("oauth state not found")
	ErrUserNotFound        = errors.New("user not found")
	ErrProjectNotFound     = errors.New("project not found")
	ErrRefreshReplay       = errors.New("refresh token replay detected")
	ErrRefreshExpired      = errors.New("refresh token expired")
	ErrSessionRevoked      = errors.New("session revoked")
)
