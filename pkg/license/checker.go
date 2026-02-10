package license

import (
	"context"
	"crypto/rsa"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// CheckResult represents the result of a license check.
type CheckResult struct {
	Valid   bool
	Token   *LicenseToken
	Message string
}

// Checker reads ConfigMap and verifies the license for this plugin.
type Checker struct {
	clientset kubernetes.Interface
	publicKey *rsa.PublicKey
	pluginID  string
	namespace string
	configMap string
	dataKey   string
	logger    *logrus.Logger

	mu     sync.RWMutex
	result *CheckResult
}

// CheckerConfig holds the configuration for the license checker.
type CheckerConfig struct {
	Clientset kubernetes.Interface
	PublicKey  *rsa.PublicKey
	PluginID  string
	Namespace string
	ConfigMap string
	DataKey   string
	Logger    *logrus.Logger
}

// NewChecker creates a new license checker.
func NewChecker(cfg CheckerConfig) *Checker {
	if cfg.Logger == nil {
		cfg.Logger = logrus.New()
	}
	return &Checker{
		clientset: cfg.Clientset,
		publicKey: cfg.PublicKey,
		pluginID:  cfg.PluginID,
		namespace: cfg.Namespace,
		configMap: cfg.ConfigMap,
		dataKey:   cfg.DataKey,
		logger:    cfg.Logger,
	}
}

// Check performs a one-time license verification.
func (c *Checker) Check(ctx context.Context) *CheckResult {
	// Read ConfigMap
	cm, err := c.clientset.CoreV1().ConfigMaps(c.namespace).Get(ctx, c.configMap, metav1.GetOptions{})
	if err != nil {
		result := &CheckResult{Valid: false, Message: fmt.Sprintf("failed to read ConfigMap %s/%s: %v", c.namespace, c.configMap, err)}
		c.setResult(result)
		return result
	}

	licenseJSON, ok := cm.Data[c.dataKey]
	if !ok || licenseJSON == "" {
		result := &CheckResult{Valid: false, Message: "no license data found in ConfigMap"}
		c.setResult(result)
		return result
	}

	// Parse token
	token, err := ParseLicenseToken([]byte(licenseJSON))
	if err != nil {
		result := &CheckResult{Valid: false, Message: fmt.Sprintf("failed to parse license: %v", err)}
		c.setResult(result)
		return result
	}

	// Verify RSA signature
	valid, err := token.Verify(c.publicKey)
	if err != nil || !valid {
		result := &CheckResult{Valid: false, Token: token, Message: "invalid license signature"}
		c.setResult(result)
		return result
	}

	// Check expiration
	if token.IsExpired() {
		result := &CheckResult{Valid: false, Token: token, Message: "license expired"}
		c.setResult(result)
		return result
	}

	// Check plugin is allowed
	if !token.IsPluginAllowed(c.pluginID) {
		result := &CheckResult{Valid: false, Token: token, Message: fmt.Sprintf("plugin %s is not authorized", c.pluginID)}
		c.setResult(result)
		return result
	}

	result := &CheckResult{Valid: true, Token: token, Message: "license valid"}
	c.setResult(result)
	return result
}

// StartPeriodicCheck starts a goroutine that re-checks the license periodically.
func (c *Checker) StartPeriodicCheck(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				result := c.Check(ctx)
				if !result.Valid {
					c.logger.WithField("message", result.Message).Warn("License re-check failed")
				} else {
					c.logger.Debug("License re-check passed")
				}
			}
		}
	}()
}

// GetResult returns the latest cached check result.
func (c *Checker) GetResult() *CheckResult {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.result
}

// IsValid returns whether the current license is valid (from cache).
func (c *Checker) IsValid() bool {
	r := c.GetResult()
	return r != nil && r.Valid
}

func (c *Checker) setResult(result *CheckResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.result = result
}
