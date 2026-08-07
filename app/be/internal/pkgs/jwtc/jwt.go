package jwtc

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Config struct {
	PrivateKeyPath string
	PublicKeyPath  string
	Issuer         string
	Audience       string
	TTL            time.Duration
}

type Claims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

type Manager struct {
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
	issuer     string
	audience   string
	ttl        time.Duration
}

func NewManager(cfg Config) (*Manager, error) {
	if strings.TrimSpace(cfg.PrivateKeyPath) == "" {
		return nil, fmt.Errorf("private key path is required")
	}
	if strings.TrimSpace(cfg.Issuer) == "" {
		return nil, fmt.Errorf("issuer is required")
	}
	if strings.TrimSpace(cfg.Audience) == "" {
		return nil, fmt.Errorf("audience is required")
	}
	if cfg.TTL <= 0 {
		return nil, fmt.Errorf("ttl must be greater than 0")
	}

	privateKey, err := loadRSAPrivateKey(cfg.PrivateKeyPath)
	if err != nil {
		return nil, err
	}

	publicKey := &privateKey.PublicKey
	if strings.TrimSpace(cfg.PublicKeyPath) != "" {
		loadedPublicKey, publicErr := loadRSAPublicKey(cfg.PublicKeyPath)
		if publicErr != nil {
			return nil, publicErr
		}
		publicKey = loadedPublicKey
	} else {
		candidatePublicKey := filepath.Join(filepath.Dir(cfg.PrivateKeyPath), "public.pem")
		if _, statErr := os.Stat(candidatePublicKey); statErr == nil {
			loadedPublicKey, publicErr := loadRSAPublicKey(candidatePublicKey)
			if publicErr != nil {
				return nil, publicErr
			}
			publicKey = loadedPublicKey
		}
	}

	return &Manager{
		privateKey: privateKey,
		publicKey:  publicKey,
		issuer:     cfg.Issuer,
		audience:   cfg.Audience,
		ttl:        cfg.TTL,
	}, nil
}

func (m *Manager) SignAccessToken(userID, sessionID string, now time.Time) (string, time.Time, error) {
	if strings.TrimSpace(userID) == "" {
		return "", time.Time{}, fmt.Errorf("user id is required")
	}
	if strings.TrimSpace(sessionID) == "" {
		return "", time.Time{}, fmt.Errorf("session id is required")
	}
	issuedAt := now.UTC()
	expiresAt := issuedAt.Add(m.ttl)
	claims := Claims{
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   userID,
			Audience:  []string{m.audience},
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			NotBefore: jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signedToken, err := token.SignedString(m.privateKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return signedToken, expiresAt, nil
}

func (m *Manager) VerifyAccessToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodRS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return m.publicKey, nil
	}, jwt.WithAudience(m.audience), jwt.WithIssuer(m.issuer))
	if err != nil {
		return nil, fmt.Errorf("verify access token: %w", err)
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid access token")
	}
	if strings.TrimSpace(claims.SessionID) == "" || strings.TrimSpace(claims.Subject) == "" {
		return nil, fmt.Errorf("invalid access token claims")
	}
	return claims, nil
}

func loadRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key file: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("invalid private key pem")
	}
	if key, parseErr := x509.ParsePKCS1PrivateKey(block.Bytes); parseErr == nil {
		return key, nil
	}
	parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
	if parseErr != nil {
		return nil, fmt.Errorf("parse private key: %w", parseErr)
	}
	privateKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not RSA")
	}
	return privateKey, nil
}

func loadRSAPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read public key file: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("invalid public key pem")
	}
	parsed, parseErr := x509.ParsePKIXPublicKey(block.Bytes)
	if parseErr != nil {
		return nil, fmt.Errorf("parse public key: %w", parseErr)
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not RSA")
	}
	return publicKey, nil
}
