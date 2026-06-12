package service

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
	"github.com/sirupsen/logrus"
)

type SLAHealthConfigStore interface {
	ListSLAHealthConfigs(ctx context.Context) ([]model.SLAConfig, error)
	RecordSLAHealthCheck(ctx context.Context, sample model.SLAHealthSample) error
}

type SLAHealthChecker struct {
	store    SLAHealthConfigStore
	client   *http.Client
	interval time.Duration
	now      func() time.Time
	logger   *logrus.Logger
}

func NewSLAHealthChecker(store SLAHealthConfigStore, logger *logrus.Logger) *SLAHealthChecker {
	return &SLAHealthChecker{
		store:    store,
		client:   &http.Client{Timeout: 3 * time.Second},
		interval: 10 * time.Second,
		now:      time.Now,
		logger:   logger,
	}
}

func (c *SLAHealthChecker) Start(ctx context.Context) {
	if c == nil || c.store == nil {
		return
	}
	go c.loop(ctx)
}

func (c *SLAHealthChecker) loop(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	c.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.runOnce(ctx)
		}
	}
}

func (c *SLAHealthChecker) runOnce(ctx context.Context) {
	configs, err := c.store.ListSLAHealthConfigs(ctx)
	if err != nil {
		if c.logger != nil {
			c.logger.WithError(err).Warn("list sla health configs failed")
		}
		return
	}
	for _, cfg := range configs {
		if !cfg.Enabled || strings.TrimSpace(cfg.URL) == "" {
			continue
		}
		sample := c.check(ctx, cfg)
		if err := c.store.RecordSLAHealthCheck(ctx, sample); err != nil && c.logger != nil {
			c.logger.WithError(err).WithField("app_id", cfg.AppID).Warn("record sla health check failed")
		}
	}
}

func (c *SLAHealthChecker) check(ctx context.Context, cfg model.SLAConfig) model.SLAHealthSample {
	started := c.now()
	sample := model.SLAHealthSample{
		AppID:     cfg.AppID,
		CheckedAt: started.Unix(),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.URL, nil)
	if err != nil {
		sample.ErrorType = "unknown_error"
		return sample
	}
	resp, err := c.client.Do(req)
	sample.LatencyMs = float64(c.now().Sub(started).Milliseconds())
	if err != nil {
		sample.ErrorType = classifyHealthCheckError(err)
		return sample
	}
	defer resp.Body.Close()
	sample.StatusCode = resp.StatusCode
	sample.Success = resp.StatusCode >= cfg.SuccessStatusMin && resp.StatusCode <= cfg.SuccessStatusMax
	if !sample.Success {
		if resp.StatusCode >= 500 {
			sample.ErrorType = "status_code_5xx"
		} else {
			sample.ErrorType = "status_code_4xx"
		}
	}
	return sample
}

func ValidateSLAHealthURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return errors.New("url is required")
	}
	if len(rawURL) > 2048 {
		return errors.New("url is too long")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("url must be absolute")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("url must use http or https")
	}
	return nil
}

func classifyHealthCheckError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns_error"
	}
	var tlsErr tls.RecordHeaderError
	if errors.As(err, &tlsErr) {
		return "tls_error"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "connection_error"
	}
	return "unknown_error"
}
