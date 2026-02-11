package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

// WriteTestRSAKeys generates an RSA key pair and writes PEM files under dir.
func WriteTestRSAKeys(dir string) (string, string, error) {
	if dir == "" {
		return "", "", fmt.Errorf("dir is required")
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", fmt.Errorf("generate rsa key failed: %w", err)
	}

	privateBytes := x509.MarshalPKCS1PrivateKey(key)
	publicBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", fmt.Errorf("marshal public key failed: %w", err)
	}

	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privateBytes})
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicBytes})

	privatePath := filepath.Join(dir, "private.pem")
	publicPath := filepath.Join(dir, "public.pem")

	if err := os.WriteFile(privatePath, privatePEM, 0o600); err != nil {
		return "", "", fmt.Errorf("write private key failed: %w", err)
	}
	if err := os.WriteFile(publicPath, publicPEM, 0o644); err != nil {
		return "", "", fmt.Errorf("write public key failed: %w", err)
	}

	return privatePath, publicPath, nil
}
