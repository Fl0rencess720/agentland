package sandboxtoken

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

var rawBase64URL = base64.RawURLEncoding

type SignerConfig struct {
	PrivateKeyPath string
	Issuer         string
	Audience       string
	KID            string
	TTL            time.Duration
}

type VerifierConfig struct {
	PublicKeyPath string
	Issuer        string
	Audience      string
	ClockSkew     time.Duration
}

type Signer struct {
	privateKey *rsa.PrivateKey
	issuer     string
	audience   string
	kid        string
	ttl        time.Duration
	now        func() time.Time
}

type Verifier struct {
	publicKey *rsa.PublicKey
	issuer    string
	audience  string
	clockSkew time.Duration
	now       func() time.Time
}

type Claims struct {
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	SessionID string `json:"sid"`
	Subject   string `json:"sub,omitempty"`
	Version   int64  `json:"ver,omitempty"`
	IssuedAt  int64  `json:"iat"`
	NotBefore int64  `json:"nbf"`
	ExpiresAt int64  `json:"exp"`
	JWTID     string `json:"jti"`
}

type Header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	KID string `json:"kid,omitempty"`
}

func NewSignerFromConfig(cfg SignerConfig) (*Signer, error) {
	if cfg.PrivateKeyPath == "" {
		return nil, errors.New("private key path is required")
	}
	if cfg.Issuer == "" {
		return nil, errors.New("issuer is required")
	}
	if cfg.Audience == "" {
		return nil, errors.New("audience is required")
	}
	if cfg.TTL <= 0 {
		return nil, errors.New("ttl must be greater than 0")
	}

	privateKey, err := loadRSAPrivateKey(cfg.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load private key failed: %w", err)
	}

	return &Signer{
		privateKey: privateKey,
		issuer:     cfg.Issuer,
		audience:   cfg.Audience,
		kid:        cfg.KID,
		ttl:        cfg.TTL,
		now:        time.Now,
	}, nil
}

func NewVerifierFromConfig(cfg VerifierConfig) (*Verifier, error) {
	if cfg.PublicKeyPath == "" {
		return nil, errors.New("public key path is required")
	}
	if cfg.Issuer == "" {
		return nil, errors.New("issuer is required")
	}
	if cfg.Audience == "" {
		return nil, errors.New("audience is required")
	}
	if cfg.ClockSkew < 0 {
		return nil, errors.New("clock skew cannot be negative")
	}

	publicKey, err := loadRSAPublicKey(cfg.PublicKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load public key failed: %w", err)
	}

	return &Verifier{
		publicKey: publicKey,
		issuer:    cfg.Issuer,
		audience:  cfg.Audience,
		clockSkew: cfg.ClockSkew,
		now:       time.Now,
	}, nil
}

func (s *Signer) Sign(sessionID, subject string, version int64) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", errors.New("session id is required")
	}

	now := s.now().UTC()
	claims := Claims{
		Issuer:    s.issuer,
		Audience:  s.audience,
		SessionID: sessionID,
		Subject:   subject,
		Version:   version,
		IssuedAt:  now.Unix(),
		NotBefore: now.Unix(),
		ExpiresAt: now.Add(s.ttl).Unix(),
		JWTID:     randomID(),
	}

	header := Header{
		Alg: "RS256",
		Typ: "JWT",
		KID: s.kid,
	}

	return signToken(s.privateKey, header, claims)
}

func (v *Verifier) Verify(token string) (*Claims, error) {
	header, claims, signature, signingInput, err := parseToken(token)
	if err != nil {
		return nil, err
	}

	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported alg: %s", header.Alg)
	}

	hash := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(v.publicKey, crypto.SHA256, hash[:], signature); err != nil {
		return nil, fmt.Errorf("verify signature failed: %w", err)
	}

	if err := v.validateClaims(claims); err != nil {
		return nil, err
	}

	return claims, nil
}

func ParseBearerToken(headerValue string) (string, error) {
	parts := strings.Fields(strings.TrimSpace(headerValue))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", errors.New("invalid authorization header format")
	}
	if parts[1] == "" {
		return "", errors.New("bearer token is empty")
	}
	return parts[1], nil
}

func (v *Verifier) validateClaims(claims *Claims) error {
	if claims == nil {
		return errors.New("claims is nil")
	}
	if claims.Issuer != v.issuer {
		return fmt.Errorf("issuer mismatch: got %q", claims.Issuer)
	}
	if claims.Audience != v.audience {
		return fmt.Errorf("audience mismatch: got %q", claims.Audience)
	}
	if strings.TrimSpace(claims.SessionID) == "" {
		return errors.New("sid claim is required")
	}

	now := v.now().UTC()
	if claims.NotBefore > 0 {
		nbf := time.Unix(claims.NotBefore, 0).UTC()
		if now.Add(v.clockSkew).Before(nbf) {
			return errors.New("token is not valid yet")
		}
	}
	if claims.IssuedAt > 0 {
		iat := time.Unix(claims.IssuedAt, 0).UTC()
		if now.Add(v.clockSkew).Before(iat) {
			return errors.New("token issued in the future")
		}
	}
	if claims.ExpiresAt <= 0 {
		return errors.New("exp claim is required")
	}
	exp := time.Unix(claims.ExpiresAt, 0).UTC()
	if !now.Add(-v.clockSkew).Before(exp) {
		return errors.New("token has expired")
	}

	return nil
}

func signToken(privateKey *rsa.PrivateKey, header Header, claims Claims) (string, error) {
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("marshal header failed: %w", err)
	}
	claimsBytes, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims failed: %w", err)
	}

	signingInput := rawBase64URL.EncodeToString(headerBytes) + "." + rawBase64URL.EncodeToString(claimsBytes)
	hash := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("sign token failed: %w", err)
	}

	return signingInput + "." + rawBase64URL.EncodeToString(signature), nil
}

func parseToken(token string) (*Header, *Claims, []byte, string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, nil, nil, "", errors.New("token format is invalid")
	}
	signingInput := parts[0] + "." + parts[1]

	headerBytes, err := rawBase64URL.DecodeString(parts[0])
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("decode header failed: %w", err)
	}
	claimsBytes, err := rawBase64URL.DecodeString(parts[1])
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("decode claims failed: %w", err)
	}
	signature, err := rawBase64URL.DecodeString(parts[2])
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("decode signature failed: %w", err)
	}

	var header Header
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, nil, nil, "", fmt.Errorf("unmarshal header failed: %w", err)
	}
	var claims Claims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return nil, nil, nil, "", fmt.Errorf("unmarshal claims failed: %w", err)
	}

	return &header, &claims, signature, signingInput, nil
}

func loadRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key file failed: %w", err)
	}
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid private key pem")
	}
	if len(bytes.TrimSpace(rest)) > 0 {
		return nil, errors.New("extra data found in private key pem")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key failed: %w", err)
	}
	key, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return key, nil
}

func loadRSAPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read public key file failed: %w", err)
	}
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid public key pem")
	}
	if len(bytes.TrimSpace(rest)) > 0 {
		return nil, errors.New("extra data found in public key pem")
	}

	if pubAny, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if pub, ok := pubAny.(*rsa.PublicKey); ok {
			return pub, nil
		}
		return nil, errors.New("public key is not RSA")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key failed: %w", err)
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("certificate public key is not RSA")
	}
	return pub, nil
}

func randomID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// token id is non-critical; fallback keeps signer functional.
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return rawBase64URL.EncodeToString(buf)
}
