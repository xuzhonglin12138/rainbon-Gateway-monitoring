package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGrafanaProxyReturnsServiceUnavailableWhenNotConfigured(t *testing.T) {
	t.Setenv("NM_SKIP_LICENSE_CHECK", "true")
	s := New(Config{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/grafana/d/cluster-overview?orgId=1", nil)
	s.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s; want 503", rec.Code, rec.Body.String())
	}
}

func TestGrafanaProxyForwardsTrimmedPathAndQuery(t *testing.T) {
	t.Setenv("NM_SKIP_LICENSE_CHECK", "true")
	var gotPath string
	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	s := New(Config{GrafanaBaseURL: upstream.URL})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/grafana/d/cluster-overview?orgId=1&refresh=1m", nil)
	s.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", rec.Code, rec.Body.String())
	}
	if gotPath != "/d/cluster-overview" {
		t.Fatalf("upstream path = %q; want /d/cluster-overview", gotPath)
	}
	if gotQuery != "orgId=1&refresh=1m" {
		t.Fatalf("upstream query = %q; want orgId=1&refresh=1m", gotQuery)
	}
}

func TestGrafanaProxyRewritesLocationAndCookiePath(t *testing.T) {
	t.Setenv("NM_SKIP_LICENSE_CHECK", "true")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/login")
		w.Header().Add("Set-Cookie", "grafana_session=abc; Path=/; HttpOnly")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()

	s := New(Config{GrafanaBaseURL: upstream.URL})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/grafana/", nil)
	s.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%s; want 302", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/grafana/login" {
		t.Fatalf("Location = %q; want /grafana/login", got)
	}
	cookie := rec.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, "Path=/grafana") {
		t.Fatalf("Set-Cookie = %q; want Path=/grafana", cookie)
	}
}

func TestGrafanaPathHelpers(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/grafana", want: "/"},
		{path: "/grafana/", want: "/"},
		{path: "/grafana/public/build/app.js", want: "/public/build/app.js"},
	}
	for _, tt := range tests {
		if got := grafanaUpstreamPath(tt.path); got != tt.want {
			t.Fatalf("grafanaUpstreamPath(%q) = %q; want %q", tt.path, got, tt.want)
		}
	}
}

func TestRewriteGrafanaLocation(t *testing.T) {
	tests := []struct {
		location string
		want     string
	}{
		{location: "/login", want: "/grafana/login"},
		{location: "login?redirect=%2F", want: "/grafana/login?redirect=%2F"},
		{location: "http://grafana.local/d/cluster-overview?orgId=1", want: "/grafana/d/cluster-overview?orgId=1"},
		{location: "/grafana/login", want: "/grafana/login"},
	}
	for _, tt := range tests {
		if got := rewriteGrafanaLocation(tt.location); got != tt.want {
			t.Fatalf("rewriteGrafanaLocation(%q) = %q; want %q", tt.location, got, tt.want)
		}
	}
}
