package server

import (
	"context"
	"embed"
	"net/http"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/license"
	"github.com/sirupsen/logrus"
)

// Server serves the embedded static files and provides health checks.
type Server struct {
	httpServer *http.Server
	checker    *license.Checker
	staticFS   embed.FS
	logger     *logrus.Logger
}

// Config holds the server configuration.
type Config struct {
	Addr     string
	Checker  *license.Checker
	StaticFS embed.FS
	Logger   *logrus.Logger
}

// New creates a new plugin HTTP server.
func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = logrus.New()
	}

	s := &Server{
		checker:  cfg.Checker,
		staticFS: cfg.StaticFS,
		logger:   cfg.Logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/static/main.js", s.handleStaticJS)
	mux.HandleFunc("/healthz", s.handleHealthz)

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
