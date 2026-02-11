package sandboxjwt

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	defaultIdentitySecretName    = "gateway-sandbox-jwt-identity"
	defaultPublicSecretName      = "gateway-sandbox-jwt-public-key"
	defaultPublicSecretNamespace = "agentland-sandboxes"
	defaultLocalPrivateKeyPath   = "/tmp/agentland/jwt/private.pem"
	defaultRSAKeyBits            = 2048

	privateKeyDataKey = "private.pem"
	publicKeyDataKey  = "public.pem"

	serviceAccountNamespacePath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

// BootstrapConfig controls how gateway signing keys are loaded/generated and persisted.
type BootstrapConfig struct {
	IdentitySecretName      string
	IdentitySecretNamespace string
	PublicSecretName        string
	PublicSecretNamespace   string
	LocalPrivateKeyPath     string
	RSAKeyBits              int
}

func EnsureGatewaySigningKey(ctx context.Context, cfg BootstrapConfig) (string, error) {
	resolved := withDefaults(cfg)

	restCfg, err := rest.InClusterConfig()
	if err != nil {
		if data, readErr := os.ReadFile(resolved.LocalPrivateKeyPath); readErr == nil {
			if _, parseErr := parseRSAPrivateKeyPEM(data); parseErr == nil {
				return resolved.LocalPrivateKeyPath, nil
			}
		}

		privatePEM, err := generatePrivateKeyPEM(resolved.RSAKeyBits)
		if err != nil {
			return "", fmt.Errorf("generate local private key failed: %w", err)
		}
		if err := writePrivateKeyFile(resolved.LocalPrivateKeyPath, privatePEM); err != nil {
			return "", fmt.Errorf("write local private key failed: %w", err)
		}
		return resolved.LocalPrivateKeyPath, nil
	}

	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return "", fmt.Errorf("create kubernetes client failed: %w", err)
	}

	privatePEM, publicPEM, err := ensureIdentitySecret(ctx, clientset, resolved)
	if err != nil {
		return "", err
	}

	if err := ensurePublicKeySecret(ctx, clientset, resolved.PublicSecretNamespace, resolved.PublicSecretName, publicPEM); err != nil {
		return "", err
	}

	if err := writePrivateKeyFile(resolved.LocalPrivateKeyPath, privatePEM); err != nil {
		return "", fmt.Errorf("write cached private key failed: %w", err)
	}

	return resolved.LocalPrivateKeyPath, nil
}

func withDefaults(cfg BootstrapConfig) BootstrapConfig {
	resolved := cfg
	if strings.TrimSpace(resolved.IdentitySecretName) == "" {
		resolved.IdentitySecretName = defaultIdentitySecretName
	}
	if strings.TrimSpace(resolved.IdentitySecretNamespace) == "" {
		resolved.IdentitySecretNamespace = currentNamespaceOrDefault("agentland-system")
	}
	if strings.TrimSpace(resolved.PublicSecretName) == "" {
		resolved.PublicSecretName = defaultPublicSecretName
	}
	if strings.TrimSpace(resolved.PublicSecretNamespace) == "" {
		resolved.PublicSecretNamespace = defaultPublicSecretNamespace
	}
	if strings.TrimSpace(resolved.LocalPrivateKeyPath) == "" {
		resolved.LocalPrivateKeyPath = defaultLocalPrivateKeyPath
	}
	if resolved.RSAKeyBits <= 0 {
		resolved.RSAKeyBits = defaultRSAKeyBits
	}
	return resolved
}

func currentNamespaceOrDefault(defaultNS string) string {
	if ns := strings.TrimSpace(os.Getenv("AL_GATEWAY_NAMESPACE")); ns != "" {
		return ns
	}
	data, err := os.ReadFile(serviceAccountNamespacePath)
	if err != nil {
		return defaultNS
	}
	ns := strings.TrimSpace(string(data))
	if ns == "" {
		return defaultNS
	}
	return ns
}

func ensureIdentitySecret(ctx context.Context, clientset kubernetes.Interface, cfg BootstrapConfig) ([]byte, []byte, error) {
	secretClient := clientset.CoreV1().Secrets(cfg.IdentitySecretNamespace)
	secret, err := secretClient.Get(ctx, cfg.IdentitySecretName, metav1.GetOptions{})
	if err == nil {
		privatePEM, ok := secret.Data[privateKeyDataKey]
		if !ok || len(privatePEM) == 0 {
			return nil, nil, fmt.Errorf("identity secret %s/%s missing %s", cfg.IdentitySecretNamespace, cfg.IdentitySecretName, privateKeyDataKey)
		}
		publicPEM, err := publicKeyPEMFromPrivatePEM(privatePEM)
		if err != nil {
			return nil, nil, fmt.Errorf("derive public key from identity secret failed: %w", err)
		}

		if !bytes.Equal(secret.Data[publicKeyDataKey], publicPEM) {
			updated := secret.DeepCopy()
			if updated.Data == nil {
				updated.Data = make(map[string][]byte)
			}
			updated.Data[publicKeyDataKey] = publicPEM
			if _, err := secretClient.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
				return nil, nil, fmt.Errorf("update identity secret public key failed: %w", err)
			}
		}
		return privatePEM, publicPEM, nil
	}

	if !apierrors.IsNotFound(err) {
		return nil, nil, fmt.Errorf("get identity secret %s/%s failed: %w", cfg.IdentitySecretNamespace, cfg.IdentitySecretName, err)
	}

	privatePEM, publicPEM, err := generateRSAKeyPairPEM(cfg.RSAKeyBits)
	if err != nil {
		return nil, nil, fmt.Errorf("generate identity key pair failed: %w", err)
	}

	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.IdentitySecretName,
			Namespace: cfg.IdentitySecretNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":    "gateway",
				"app.kubernetes.io/part-of": "agentland",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			privateKeyDataKey: privatePEM,
			publicKeyDataKey:  publicPEM,
		},
	}

	if _, err := secretClient.Create(ctx, newSecret, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ensureIdentitySecret(ctx, clientset, cfg)
		}
		return nil, nil, fmt.Errorf("create identity secret %s/%s failed: %w", cfg.IdentitySecretNamespace, cfg.IdentitySecretName, err)
	}

	return privatePEM, publicPEM, nil
}

func ensurePublicKeySecret(ctx context.Context, clientset kubernetes.Interface, namespace, secretName string, publicPEM []byte) error {
	secretClient := clientset.CoreV1().Secrets(namespace)
	secret, err := secretClient.Get(ctx, secretName, metav1.GetOptions{})
	if err == nil {
		if bytes.Equal(secret.Data[publicKeyDataKey], publicPEM) {
			return nil
		}
		updated := secret.DeepCopy()
		if updated.Data == nil {
			updated.Data = make(map[string][]byte)
		}
		updated.Data[publicKeyDataKey] = publicPEM
		if _, err := secretClient.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update public key secret %s/%s failed: %w", namespace, secretName, err)
		}
		return nil
	}

	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get public key secret %s/%s failed: %w", namespace, secretName, err)
	}

	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/part-of": "agentland",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{publicKeyDataKey: publicPEM},
	}
	if _, err := secretClient.Create(ctx, newSecret, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ensurePublicKeySecret(ctx, clientset, namespace, secretName, publicPEM)
		}
		return fmt.Errorf("create public key secret %s/%s failed: %w", namespace, secretName, err)
	}
	return nil
}

func writePrivateKeyFile(path string, privatePEM []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, privatePEM, 0o600)
}

func generatePrivateKeyPEM(bits int) ([]byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return nil, err
	}
	privateBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateBytes}), nil
}

func generateRSAKeyPairPEM(bits int) ([]byte, []byte, error) {
	privatePEM, err := generatePrivateKeyPEM(bits)
	if err != nil {
		return nil, nil, err
	}
	publicPEM, err := publicKeyPEMFromPrivatePEM(privatePEM)
	if err != nil {
		return nil, nil, err
	}
	return privatePEM, publicPEM, nil
}

func publicKeyPEMFromPrivatePEM(privatePEM []byte) ([]byte, error) {
	privateKey, err := parseRSAPrivateKeyPEM(privatePEM)
	if err != nil {
		return nil, err
	}
	publicBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicBytes}), nil
}

func parseRSAPrivateKeyPEM(data []byte) (*rsa.PrivateKey, error) {
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
