package license

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"time"
)

var (
	ErrInvalidPEM     = errors.New("invalid PEM format")
	ErrInvalidKeyType = errors.New("invalid key type, expected RSA public key")
)

// LicenseToken represents the Rainbond enterprise license.
type LicenseToken struct {
	Code           string   `json:"code"`
	ClusterID      string   `json:"cluster_id"`
	Company        string   `json:"company"`
	Contact        string   `json:"contact"`
	Tier           string   `json:"tier"`
	AllowedPlugins []string `json:"allowed_plugins"`
	StartAt        int64    `json:"start_at"`
	ExpireAt       int64    `json:"expire_at"`
	SubscribeUntil int64    `json:"subscribe_until"`
	ClusterLimit   int      `json:"cluster_limit"`
	NodeLimit      int      `json:"node_limit"`
	MemoryLimit    int64    `json:"memory_limit"`
	CPULimit       int64    `json:"cpu_limit"`
	Signature      string   `json:"signature"`
}

// licenseTokenForSigning excludes the Signature field for verification.
type licenseTokenForSigning struct {
	Code           string   `json:"code"`
	ClusterID      string   `json:"cluster_id"`
	Company        string   `json:"company"`
	Contact        string   `json:"contact"`
	Tier           string   `json:"tier"`
	AllowedPlugins []string `json:"allowed_plugins"`
	StartAt        int64    `json:"start_at"`
	ExpireAt       int64    `json:"expire_at"`
	SubscribeUntil int64    `json:"subscribe_until"`
	ClusterLimit   int      `json:"cluster_limit"`
	NodeLimit      int      `json:"node_limit"`
	MemoryLimit    int64    `json:"memory_limit"`
	CPULimit       int64    `json:"cpu_limit"`
}

// ParseLicenseToken parses a JSON-encoded license token.
func ParseLicenseToken(data []byte) (*LicenseToken, error) {
	var token LicenseToken
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

// SerializeForSigning returns the JSON bytes without the signature field.
func (t *LicenseToken) SerializeForSigning() ([]byte, error) {
	data := licenseTokenForSigning{
		Code:           t.Code,
		ClusterID:      t.ClusterID,
		Company:        t.Company,
		Contact:        t.Contact,
		Tier:           t.Tier,
		AllowedPlugins: t.AllowedPlugins,
		StartAt:        t.StartAt,
		ExpireAt:       t.ExpireAt,
		SubscribeUntil: t.SubscribeUntil,
		ClusterLimit:   t.ClusterLimit,
		NodeLimit:      t.NodeLimit,
		MemoryLimit:    t.MemoryLimit,
		CPULimit:       t.CPULimit,
	}
	return json.Marshal(data)
}

// Verify verifies the RSA-SHA256 signature using the given public key.
func (t *LicenseToken) Verify(publicKey *rsa.PublicKey) (bool, error) {
	data, err := t.SerializeForSigning()
	if err != nil {
		return false, err
	}

	sig, err := base64.StdEncoding.DecodeString(t.Signature)
	if err != nil {
		return false, err
	}

	hash := sha256.Sum256(data)
	err = rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, hash[:], sig)
	return err == nil, nil
}

// IsExpired returns true if the license has expired.
func (t *LicenseToken) IsExpired() bool {
	return time.Now().Unix() > t.ExpireAt
}

// IsPluginAllowed returns true if the given plugin ID is in the allowed list.
func (t *LicenseToken) IsPluginAllowed(pluginID string) bool {
	for _, p := range t.AllowedPlugins {
		if p == "*" || p == pluginID {
			return true
		}
	}
	return false
}

// DecodePublicKeyFromPEM decodes an RSA public key from PEM-encoded bytes.
func DecodePublicKeyFromPEM(pemData []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, ErrInvalidPEM
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	publicKey, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, ErrInvalidKeyType
	}

	return publicKey, nil
}
