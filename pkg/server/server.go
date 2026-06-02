package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/license"
	"github.com/goodrain/rainbond-plugin-template/pkg/model"
	"github.com/goodrain/rainbond-plugin-template/pkg/service"
	"github.com/sirupsen/logrus"
)

// Server serves the embedded static files and provides health checks.
type Server struct {
	httpServer       *http.Server
	checker          *license.Checker
	staticFS         embed.FS
	logger           *logrus.Logger
	collector        *service.InternalRouteCollector
	queryStore       RouteGroupQueryStore
	slaService       SLAService
	overviewService  OverviewService
	configStore      ConfigStore
	defaultSLATarget float64
	httpLoggerSyncer HTTPLoggerSyncer
}

// Config holds the server configuration.
type Config struct {
	Addr             string
	Checker          *license.Checker
	StaticFS         embed.FS
	Logger           *logrus.Logger
	Collector        *service.InternalRouteCollector
	QueryStore       RouteGroupQueryStore
	SLAService       SLAService
	OverviewService  OverviewService
	ConfigStore      ConfigStore
	DefaultSLATarget float64
	HTTPLoggerSyncer HTTPLoggerSyncer
}

type RouteGroupQueryStore interface {
	ListRouteGroups(ctx context.Context, scope model.AggregateScope, window model.Window, limit int, sortBy string) ([]model.RouteGroupItem, error)
}

type RouteGroupSnapshotMetaStore interface {
	GetRouteGroupSnapshotMeta(ctx context.Context, scope model.AggregateScope, window model.Window, sortBy string) (model.QueryMeta, error)
}

type SLAService interface {
	GetAppSLA(ctx context.Context, appID string, window model.Window) (model.SLAStatus, error)
}

type OverviewService interface {
	GetPlatformOverview(ctx context.Context, window model.Window) (model.Overview, error)
	GetAppOverview(ctx context.Context, appID string, window model.Window) (model.Overview, error)
	GetComponentOverview(ctx context.Context, componentID string, window model.Window) (model.Overview, error)
}

type ConfigStore interface {
	GetSLAConfig(ctx context.Context, appID string, defaultTarget float64) (model.SLAConfig, error)
	SaveSLAConfig(ctx context.Context, cfg model.SLAConfig) error
	GetRouteGroupRules(ctx context.Context, appID string) ([]model.RouteGroupRule, error)
	SaveRouteGroupRules(ctx context.Context, appID string, rules []model.RouteGroupRule) error
}

type HTTPLoggerSyncer interface {
	SyncHTTPLogger(ctx context.Context, namespace, appID string) error
}

const maxCollectorBatchSize = 5000
const maxCollectorBodyBytes = 8 << 20

// New creates a new plugin HTTP server.
func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = logrus.New()
	}
	if cfg.DefaultSLATarget <= 0 {
		cfg.DefaultSLATarget = 0.999
	}

	s := &Server{
		checker:          cfg.Checker,
		staticFS:         cfg.StaticFS,
		logger:           cfg.Logger,
		collector:        cfg.Collector,
		queryStore:       cfg.QueryStore,
		slaService:       cfg.SLAService,
		overviewService:  cfg.OverviewService,
		configStore:      cfg.ConfigStore,
		defaultSLATarget: cfg.DefaultSLATarget,
		httpLoggerSyncer: cfg.HTTPLoggerSyncer,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/static/main.js", s.handleStaticJS)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/api/v1/collector/apisix/logs", s.handleCollectApisixLogs)
	mux.HandleFunc("/api/v1/platform/internal-routes/top-errors", s.handlePlatformTopErrors)
	mux.HandleFunc("/api/v1/platform/internal-routes/top-latency", s.handlePlatformTopLatency)
	mux.HandleFunc("/api/v1/platform/overview", s.handlePlatformOverview)
	mux.HandleFunc("/api/v1/teams/", s.handleTeamRoutes)
	mux.HandleFunc("/api/v1/apps/", s.handleAppRoutes)
	mux.HandleFunc("/api/v1/components/", s.handleComponentRoutes)

	s.httpServer = &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return s
}

// Start starts the HTTP server.
func (s *Server) Start() error {
	s.logger.WithField("addr", s.httpServer.Addr).Info("Plugin HTTP server started")
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// handleStaticJS serves the embedded main.js file.
// It checks the license before serving.
func (s *Server) handleStaticJS(w http.ResponseWriter, r *http.Request) {
	if !s.checker.IsValid() {
		http.Error(w, "plugin not authorized", http.StatusForbidden)
		return
	}

	content, err := s.staticFS.ReadFile("static/main.js")
	if err != nil {
		http.Error(w, "main.js not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(content)
}

// handleHealthz returns the health status.
// Returns 200 if licensed, 503 if not.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if s.checker.IsValid() {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	} else {
		result := s.checker.GetResult()
		msg := "not licensed"
		if result != nil {
			msg = result.Message
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(msg))
	}
}

func (s *Server) handleCollectApisixLogs(w http.ResponseWriter, r *http.Request) {
	if !s.isLicensed() {
		http.Error(w, "plugin not authorized", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.collector == nil {
		http.Error(w, "collector is not configured", http.StatusServiceUnavailable)
		return
	}

	logs, err := decodeAccessLogs(http.MaxBytesReader(w, r.Body, maxCollectorBodyBytes))
	if err != nil {
		if isRequestBodyTooLarge(err.Error()) {
			http.Error(w, "collector payload is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(logs) > maxCollectorBatchSize {
		http.Error(w, "collector batch is too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := s.collector.Collect(r.Context(), logs); err != nil {
		s.logger.WithError(err).Warn("collect apisix logs failed")
		http.Error(w, "collect logs failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"data": map[string]interface{}{"accepted": len(logs)},
	})
}

func (s *Server) handlePlatformTopErrors(w http.ResponseWriter, r *http.Request) {
	s.handleTopRoutes(w, r, model.AggregateScope{Kind: model.ScopePlatform}, "errors")
}

func (s *Server) handlePlatformTopLatency(w http.ResponseWriter, r *http.Request) {
	s.handleTopRoutes(w, r, model.AggregateScope{Kind: model.ScopePlatform}, "latency")
}

func (s *Server) handlePlatformOverview(w http.ResponseWriter, r *http.Request) {
	s.handleOverview(w, r, model.AggregateScope{Kind: model.ScopePlatform})
}

func (s *Server) handleTeamRoutes(w http.ResponseWriter, r *http.Request) {
	id, suffix, ok := splitScopedPath(r.URL.Path, "/api/v1/teams/")
	if !ok || !strings.HasPrefix(suffix, "/internal-routes/") {
		http.NotFound(w, r)
		return
	}
	sortBy := "requests"
	if strings.HasSuffix(suffix, "/top-errors") {
		sortBy = "errors"
	}
	if strings.HasSuffix(suffix, "/top-latency") {
		sortBy = "latency"
	}
	s.handleTopRoutes(w, r, model.AggregateScope{Kind: model.ScopeTeam, ID: id}, sortBy)
}

func (s *Server) handleAppRoutes(w http.ResponseWriter, r *http.Request) {
	id, suffix, ok := splitScopedPath(r.URL.Path, "/api/v1/apps/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if suffix == "/sla/config" {
		s.handleAppSLAConfig(w, r, id)
		return
	}
	if suffix == "/sla" {
		s.handleAppSLA(w, r, id)
		return
	}
	if suffix == "/overview" {
		s.handleOverview(w, r, model.AggregateScope{Kind: model.ScopeApp, ID: id})
		return
	}
	if suffix == "/gateway/http-logger/sync" {
		s.handleAppHTTPLoggerSync(w, r, id)
		return
	}
	if !strings.HasPrefix(suffix, "/internal-routes/") {
		http.NotFound(w, r)
		return
	}
	if suffix == "/internal-routes/rules" {
		s.handleAppRouteGroupRules(w, r, id)
		return
	}
	sortBy := "requests"
	if strings.HasSuffix(suffix, "/top-errors") {
		sortBy = "errors"
	}
	if strings.HasSuffix(suffix, "/top-latency") {
		sortBy = "latency"
	}
	if strings.HasSuffix(suffix, "/summary") {
		sortBy = "summary"
	}
	s.handleTopRoutes(w, r, model.AggregateScope{Kind: model.ScopeApp, ID: id}, sortBy)
}

func (s *Server) handleAppSLA(w http.ResponseWriter, r *http.Request, appID string) {
	if !s.isLicensed() {
		http.Error(w, "plugin not authorized", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.slaService == nil {
		http.Error(w, "sla service is not configured", http.StatusServiceUnavailable)
		return
	}
	window, err := model.ParseWindow(r.URL.Query().Get("window"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	status, err := s.slaService.GetAppSLA(r.Context(), appID, window)
	if err != nil {
		s.logger.WithError(err).Warn("get app sla failed")
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"data":     nil,
			"warnings": []string{"prometheus sla query is unavailable"},
			"meta": model.QueryMeta{
				Window:  window,
				Partial: true,
				Stale:   true,
			},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data":     status,
		"warnings": []string{},
		"meta":     model.QueryMeta{Window: window},
	})
}

func (s *Server) handleAppSLAConfig(w http.ResponseWriter, r *http.Request, appID string) {
	if !s.isLicensed() {
		http.Error(w, "plugin not authorized", http.StatusForbidden)
		return
	}
	if s.configStore == nil {
		http.Error(w, "config store is not configured", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg, err := s.configStore.GetSLAConfig(r.Context(), appID, s.defaultSLATarget)
		if err != nil {
			s.logger.WithError(err).Warn("get sla config failed")
			http.Error(w, "get sla config failed", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"data": cfg})
	case http.MethodPut:
		var payload model.SLAConfig
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid sla config payload"})
			return
		}
		payload.AppID = appID
		if payload.Target <= 0 || payload.Target > 1 {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "target must be in (0,1]"})
			return
		}
		if err := s.configStore.SaveSLAConfig(r.Context(), payload); err != nil {
			s.logger.WithError(err).Warn("save sla config failed")
			http.Error(w, "save sla config failed", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"data": payload})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAppHTTPLoggerSync(w http.ResponseWriter, r *http.Request, appID string) {
	if !s.isLicensed() {
		http.Error(w, "plugin not authorized", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.httpLoggerSyncer == nil {
		http.Error(w, "http logger syncer is not configured", http.StatusServiceUnavailable)
		return
	}
	var payload struct {
		Namespace   string `json:"namespace"`
		RegionAppID string `json:"region_app_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid http logger sync payload"})
		return
	}
	payload.Namespace = strings.TrimSpace(payload.Namespace)
	payload.RegionAppID = strings.TrimSpace(payload.RegionAppID)
	if payload.Namespace == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "namespace is required"})
		return
	}
	syncAppID := payload.RegionAppID
	if syncAppID == "" {
		syncAppID = appID
	}
	if err := s.httpLoggerSyncer.SyncHTTPLogger(r.Context(), payload.Namespace, syncAppID); err != nil {
		s.logger.WithError(err).Warn("sync app http logger failed")
		http.Error(w, "sync app http logger failed", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": map[string]string{
			"namespace": payload.Namespace,
			"app_id":    syncAppID,
		},
	})
}

func (s *Server) handleComponentRoutes(w http.ResponseWriter, r *http.Request) {
	id, suffix, ok := splitScopedPath(r.URL.Path, "/api/v1/components/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if suffix == "/overview" {
		s.handleOverview(w, r, model.AggregateScope{Kind: model.ScopeComponent, ID: id})
		return
	}
	if !strings.HasPrefix(suffix, "/internal-routes") {
		http.NotFound(w, r)
		return
	}
	s.handleTopRoutes(w, r, model.AggregateScope{Kind: model.ScopeComponent, ID: id}, "requests")
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request, scope model.AggregateScope) {
	if !s.isLicensed() {
		http.Error(w, "plugin not authorized", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.overviewService == nil {
		http.Error(w, "overview service is not configured", http.StatusServiceUnavailable)
		return
	}
	window, err := model.ParseWindow(r.URL.Query().Get("window"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	var overview model.Overview
	switch scope.Kind {
	case model.ScopePlatform:
		overview, err = s.overviewService.GetPlatformOverview(r.Context(), window)
	case model.ScopeApp:
		overview, err = s.overviewService.GetAppOverview(r.Context(), scope.ID, window)
	case model.ScopeComponent:
		overview, err = s.overviewService.GetComponentOverview(r.Context(), scope.ID, window)
	default:
		err = fmt.Errorf("unsupported overview scope %s", scope.Kind)
	}
	if err != nil {
		s.logger.WithError(err).Warn("get overview failed")
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"data":     nil,
			"warnings": []string{"prometheus overview query is unavailable"},
			"meta": model.QueryMeta{
				Window:  window,
				Partial: true,
				Stale:   true,
			},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data":     overview,
		"warnings": []string{},
		"meta":     model.QueryMeta{Window: window},
	})
}

func (s *Server) handleAppRouteGroupRules(w http.ResponseWriter, r *http.Request, appID string) {
	if !s.isLicensed() {
		http.Error(w, "plugin not authorized", http.StatusForbidden)
		return
	}
	if s.configStore == nil {
		http.Error(w, "config store is not configured", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		rules, err := s.configStore.GetRouteGroupRules(r.Context(), appID)
		if err != nil {
			s.logger.WithError(err).Warn("get route group rules failed")
			http.Error(w, "get route group rules failed", http.StatusServiceUnavailable)
			return
		}
		if rules == nil {
			rules = []model.RouteGroupRule{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"data": rules})
	case http.MethodPut:
		var payload struct {
			Rules []model.RouteGroupRule `json:"rules"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid route group rules payload"})
			return
		}
		if err := validateRouteGroupRules(payload.Rules); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
			return
		}
		if err := s.configStore.SaveRouteGroupRules(r.Context(), appID, payload.Rules); err != nil {
			s.logger.WithError(err).Warn("save route group rules failed")
			http.Error(w, "save route group rules failed", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"data": payload.Rules})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleTopRoutes(w http.ResponseWriter, r *http.Request, scope model.AggregateScope, sortBy string) {
	if !s.isLicensed() {
		http.Error(w, "plugin not authorized", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.queryStore == nil {
		http.Error(w, "route group store is not configured", http.StatusServiceUnavailable)
		return
	}
	window, err := model.ParseWindow(r.URL.Query().Get("window"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"), 50)
	items, err := s.queryStore.ListRouteGroups(r.Context(), scope, window, limit, sortBy)
	if err != nil {
		s.logger.WithError(err).Warn("list route group top failed")
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"data":     []model.RouteGroupItem{},
			"warnings": []string{"redis route group snapshot is unavailable"},
			"meta": model.QueryMeta{
				Window:  window,
				Partial: true,
				Stale:   true,
			},
		})
		return
	}
	meta := model.QueryMeta{Window: window}
	if metaStore, ok := s.queryStore.(RouteGroupSnapshotMetaStore); ok {
		if snapshotMeta, err := metaStore.GetRouteGroupSnapshotMeta(r.Context(), scope, window, sortBy); err == nil {
			meta = snapshotMeta
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data":     items,
		"meta":     meta,
		"warnings": []string{},
	})
}

func splitScopedPath(path, prefix string) (string, string, bool) {
	rest := strings.TrimPrefix(path, prefix)
	if rest == path || rest == "" {
		return "", "", false
	}
	parts := strings.SplitN(rest, "/", 2)
	if parts[0] == "" {
		return "", "", false
	}
	suffix := ""
	if len(parts) == 2 {
		suffix = "/" + parts[1]
	}
	return parts[0], suffix, true
}

func parseLimit(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	if value > 200 {
		return 200
	}
	return value
}

func validateRouteGroupRules(rules []model.RouteGroupRule) error {
	if len(rules) > 200 {
		return fmt.Errorf("too many route group rules")
	}
	for _, rule := range rules {
		if strings.TrimSpace(rule.Prefix) == "" {
			return fmt.Errorf("route group rule prefix is required")
		}
		if strings.TrimSpace(rule.Group) == "" {
			return fmt.Errorf("route group rule group is required")
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		fmt.Fprintf(w, `{"error":"encode response failed"}`)
	}
}

func decodeAccessLogs(body io.Reader) ([]model.ApisixAccessLog, error) {
	payload, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read collector payload: %w", err)
	}
	var logs []model.ApisixAccessLog
	if err := json.Unmarshal(payload, &logs); err == nil {
		return logs, nil
	}
	var single model.ApisixAccessLog
	if err := json.Unmarshal(payload, &single); err == nil {
		return []model.ApisixAccessLog{single}, nil
	}
	return nil, fmt.Errorf("collector payload must be a JSON object or array")
}

func isRequestBodyTooLarge(message string) bool {
	return strings.Contains(message, "http: request body too large")
}

func (s *Server) isLicensed() bool {
	return s.checker == nil || s.checker.IsValid()
}
