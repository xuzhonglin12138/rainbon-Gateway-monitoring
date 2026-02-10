package license

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
	"time"
)

func encodePublicKeyToPEM(pubKey *rsa.PublicKey) ([]byte, error) {
	pubBytes, err := x509.MarshalPKIXPublicKey(pubKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes}), nil
}

func generateTestKeyPair(t *testing.T) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key pair: %v", err)
	}
	return privKey, &privKey.PublicKey
}

func signTestToken(t *testing.T, token *LicenseToken, privKey *rsa.PrivateKey) {
	t.Helper()
	data, err := token.SerializeForSigning()
	if err != nil {
		t.Fatalf("failed to serialize: %v", err)
	}

	hash := sha256.Sum256(data)
	sig, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatalf("failed to sign: %v", err)
	}
	token.Signature = base64.StdEncoding.EncodeToString(sig)
}

func TestLicenseToken_Verify(t *testing.T) {
	privKey, pubKey := generateTestKeyPair(t)

	token := &LicenseToken{
		Code:           "TEST-001",
		ClusterID:      "test-cluster-id",
		Company:        "Test Company",
		Tier:           "advanced",
		AllowedPlugins: []string{"*"},
		StartAt:        time.Now().Add(-1 * time.Hour).Unix(),
		ExpireAt:       time.Now().Add(24 * time.Hour).Unix(),
		SubscribeUntil: time.Now().Add(30 * 24 * time.Hour).Unix(),
		ClusterLimit:   -1,
		NodeLimit:      -1,
		MemoryLimit:    -1,
		CPULimit:       -1,
	}
	signTestToken(t, token, privKey)

	valid, err := token.Verify(pubKey)
	if err != nil {
		t.Fatalf("verify returned error: %v", err)
	}
	if !valid {
		t.Fatal("expected valid signature")
	}
}

func TestLicenseToken_Verify_Tampered(t *testing.T) {
	privKey, pubKey := generateTestKeyPair(t)

	token := &LicenseToken{
		Code:           "TEST-001",
		ClusterID:      "test-cluster-id",
		Tier:           "advanced",
		AllowedPlugins: []string{"*"},
		StartAt:        time.Now().Add(-1 * time.Hour).Unix(),
		ExpireAt:       time.Now().Add(24 * time.Hour).Unix(),
		ClusterLimit:   -1,
		NodeLimit:      -1,
		MemoryLimit:    -1,
		CPULimit:       -1,
	}
	signTestToken(t, token, privKey)

	// Tamper with the token
	token.Tier = "basic"

	valid, err := token.Verify(pubKey)
	if err != nil {
		t.Fatalf("verify returned error: %v", err)
	}
	if valid {
		t.Fatal("expected invalid signature after tampering")
	}
}

func TestLicenseToken_IsExpired(t *testing.T) {
	token := &LicenseToken{ExpireAt: time.Now().Add(-1 * time.Hour).Unix()}
	if !token.IsExpired() {
		t.Fatal("expected expired")
	}

	token.ExpireAt = time.Now().Add(1 * time.Hour).Unix()
	if token.IsExpired() {
		t.Fatal("expected not expired")
	}
}

func TestLicenseToken_IsPluginAllowed(t *testing.T) {
	token := &LicenseToken{AllowedPlugins: []string{"plugin-a", "plugin-b"}}

	if !token.IsPluginAllowed("plugin-a") {
		t.Fatal("expected plugin-a to be allowed")
	}
	if token.IsPluginAllowed("plugin-c") {
		t.Fatal("expected plugin-c to not be allowed")
	}

	token.AllowedPlugins = []string{"*"}
	if !token.IsPluginAllowed("any-plugin") {
		t.Fatal("expected wildcard to allow any plugin")
	}
}

func TestDecodePublicKeyFromPEM(t *testing.T) {
	_, pubKey := generateTestKeyPair(t)

	// Encode to PEM
	pubBytes, err := encodePublicKeyToPEM(pubKey)
	if err != nil {
		t.Fatalf("failed to encode public key: %v", err)
	}

	// Decode back
	decoded, err := DecodePublicKeyFromPEM(pubBytes)
	if err != nil {
		t.Fatalf("failed to decode public key: %v", err)
	}
	if decoded.N.Cmp(pubKey.N) != 0 {
		t.Fatal("decoded key does not match original")
	}
}

func TestDecodePublicKeyFromPEM_Invalid(t *testing.T) {
	_, err := DecodePublicKeyFromPEM([]byte("not a pem"))
	if err == nil {
		t.Fatal("expected error for invalid PEM")
	}
}
