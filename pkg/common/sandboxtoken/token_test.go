package sandboxtoken

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/common/testutil"
	"github.com/stretchr/testify/require"
)

func TestSignerAndVerifier_Success(t *testing.T) {
	privatePath, publicPath, err := testutil.WriteTestRSAKeys(t.TempDir())
	require.NoError(t, err)

	signer, err := NewSignerFromConfig(SignerConfig{
		PrivateKeyPath: privatePath,
		Issuer:         "agentland-gateway",
		Audience:       "sandbox",
		KID:            "default",
		TTL:            5 * time.Minute,
	})
	require.NoError(t, err)
	signer.now = func() time.Time { return time.Unix(1000, 0).UTC() }

	verifier, err := NewVerifierFromConfig(VerifierConfig{
		PublicKeyPath: publicPath,
		Issuer:        "agentland-gateway",
		Audience:      "sandbox",
		ClockSkew:     30 * time.Second,
	})
	require.NoError(t, err)
	verifier.now = func() time.Time { return time.Unix(1001, 0).UTC() }

	token, err := signer.Sign("session-abc", "user-1", 2)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	claims, err := verifier.Verify(token)
	require.NoError(t, err)
	require.Equal(t, "session-abc", claims.SessionID)
	require.Equal(t, "user-1", claims.Subject)
	require.Equal(t, int64(2), claims.Version)
}

func TestVerifier_RejectExpiredToken(t *testing.T) {
	privatePath, publicPath, err := testutil.WriteTestRSAKeys(t.TempDir())
	require.NoError(t, err)

	signer, err := NewSignerFromConfig(SignerConfig{
		PrivateKeyPath: privatePath,
		Issuer:         "agentland-gateway",
		Audience:       "sandbox",
		TTL:            1 * time.Minute,
	})
	require.NoError(t, err)
	signer.now = func() time.Time { return time.Unix(2000, 0).UTC() }

	verifier, err := NewVerifierFromConfig(VerifierConfig{
		PublicKeyPath: publicPath,
		Issuer:        "agentland-gateway",
		Audience:      "sandbox",
		ClockSkew:     0,
	})
	require.NoError(t, err)
	verifier.now = func() time.Time { return time.Unix(2200, 0).UTC() }

	token, err := signer.Sign("session-abc", "", 0)
	require.NoError(t, err)

	_, err = verifier.Verify(token)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expired")
}

func TestVerifier_RejectsTamperedToken(t *testing.T) {
	privatePath, publicPath, err := testutil.WriteTestRSAKeys(t.TempDir())
	require.NoError(t, err)

	signer, err := NewSignerFromConfig(SignerConfig{
		PrivateKeyPath: privatePath,
		Issuer:         "agentland-gateway",
		Audience:       "sandbox",
		TTL:            5 * time.Minute,
	})
	require.NoError(t, err)

	verifier, err := NewVerifierFromConfig(VerifierConfig{
		PublicKeyPath: publicPath,
		Issuer:        "agentland-gateway",
		Audience:      "sandbox",
		ClockSkew:     30 * time.Second,
	})
	require.NoError(t, err)

	token, err := signer.Sign("session-abc", "", 0)
	require.NoError(t, err)

	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	parts[1] = parts[1] + "tampered"
	tampered := strings.Join(parts, ".")

	_, err = verifier.Verify(tampered)
	require.Error(t, err)
}

func TestParseBearerToken(t *testing.T) {
	token, err := ParseBearerToken("Bearer abc.def")
	require.NoError(t, err)
	require.Equal(t, "abc.def", token)

	_, err = ParseBearerToken("Basic aaa")
	require.Error(t, err)
}

func TestNewSignerFromConfig_RejectsExtraPEMData(t *testing.T) {
	privatePath, _, err := testutil.WriteTestRSAKeys(t.TempDir())
	require.NoError(t, err)

	content, err := os.ReadFile(privatePath)
	require.NoError(t, err)
	content = append(content, []byte("extra")...)
	require.NoError(t, os.WriteFile(privatePath, content, 0o600))

	_, err = NewSignerFromConfig(SignerConfig{
		PrivateKeyPath: privatePath,
		Issuer:         "agentland-gateway",
		Audience:       "sandbox",
		TTL:            5 * time.Minute,
	})
	require.Error(t, err)
}

func TestNewVerifierFromConfig_RejectsExtraPEMData(t *testing.T) {
	_, publicPath, err := testutil.WriteTestRSAKeys(t.TempDir())
	require.NoError(t, err)

	content, err := os.ReadFile(publicPath)
	require.NoError(t, err)
	content = append(content, []byte("extra")...)
	require.NoError(t, os.WriteFile(publicPath, content, 0o644))

	_, err = NewVerifierFromConfig(VerifierConfig{
		PublicKeyPath: publicPath,
		Issuer:        "agentland-gateway",
		Audience:      "sandbox",
		ClockSkew:     30 * time.Second,
	})
	require.Error(t, err)
}
