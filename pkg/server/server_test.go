package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goodrain/rainbond-plugin-template/pkg/license"
	"github.com/goodrain/rainbond-plugin-template/pkg/model"
)

type fakeConfigStore struct {
	slaConfig model.SLAConfig
	rules     []model.RouteGroupRule
}

func (f *fakeConfigStore) GetSLAConfig(_ context.Context, appID string, defaultTarget float64) (model.SLAConfig, error) {
	if f.slaConfig.AppID == "" {
		return model.SLAConfig{AppID: appID, Target: defaultTarget}, nil
	}
	return f.slaConfig, nil
}

func (f *fakeConfigStore) SaveSLAConfig(_ context.Context, cfg model.SLAConfig) error {
	f.slaConfig = cfg
	return nil
}

func (f *fakeConfigStore) GetRouteGroupRules(_ context.Context, _ string) ([]model.RouteGroupRule, error) {
	return f.rules, nil
}

func (f *fakeConfigStore) SaveRouteGroupRules(_ context.Context, _ string, rules []model.RouteGroupRule) error {
	f.rules = rules
	return nil
}

type fakeRouteGroupQueryStore struct {
	items      []model.RouteGroupItem
	components []model.AppComponentSummary
	meta       model.QueryMeta
}

func (f fakeRouteGroupQueryStore) ListRouteGroups(_ context.Context, _ model.AggregateScope, _ model.Window, _ int, _ string) ([]model.RouteGroupItem, error) {
	return f.items, nil
}

func (f fakeRouteGroupQueryStore) GetRouteGroupSnapshotMeta(_ context.Context, _ model.AggregateScope, _ model.Window, _ string) (model.QueryMeta, error) {
	return f.meta, nil
}

func (f fakeRouteGroupQueryStore) ListAppComponentSummaries(_ context.Context, _ string, _ model.Window, _ int) ([]model.AppComponentSummary, error) {
	return f.components, nil
}

type fakeHTTPLoggerSyncer struct {
	namespace    string
	appID        string
	matchAppID   string
	mappingAppID string
}

func (f *fakeHTTPLoggerSyncer) SyncHTTPLogger(_ context.Context, namespace, appID string) error {
	f.namespace = namespace
	f.appID = appID
	return nil
}

func (f *fakeHTTPLoggerSyncer) SyncHTTPLoggerForApp(_ context.Context, namespace, matchAppID, mappingAppID string) error {
	f.namespace = namespace
	f.matchAppID = matchAppID
	f.mappingAppID = mappingAppID
	return nil
}

func TestDecodeAccessLogsAcceptsBatchAndSinglePayloads(t *testing.T) {
	batch, err := decodeAccessLogs(strings.NewReader(`[{"route_id":"r1","uri":"/a"},{"route_id":"r2","uri":"/b"}]`))
	if err != nil {
		t.Fatalf("decode batch unexpected error: %v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("batch length = %d; want 2", len(batch))
	}

	single, err := decodeAccessLogs(strings.NewReader(`{"route_id":"r1","uri":"/a"}`))
	if err != nil {
		t.Fatalf("decode single unexpected error: %v", err)
	}
	if len(single) != 1 || single[0].RouteID != "r1" {
		t.Fatalf("single decode = %#v", single)
	}
}

func TestIsRequestBodyTooLarge(t *testing.T) {
	if !isRequestBodyTooLarge("read collector payload: http: request body too large") {
		t.Fatal("isRequestBodyTooLarge() = false; want true")
	}
	if isRequestBodyTooLarge("collector payload must be a JSON object or array") {
		t.Fatal("isRequestBodyTooLarge() = true; want false")
	}
}

func TestParseLimitCapsAtTwoHundred(t *testing.T) {
	if got := parseLimit("1000", 50); got != 200 {
		t.Fatalf("parseLimit cap = %d; want 200", got)
	}
	if got := parseLimit("bad", 50); got != 50 {
		t.Fatalf("parseLimit fallback = %d; want 50", got)
	}
}

func TestSplitScopedPath(t *testing.T) {
	id, suffix, ok := splitScopedPath("/api/v1/apps/app-a/internal-routes/top-errors", "/api/v1/apps/")
	if !ok {
		t.Fatal("splitScopedPath ok = false; want true")
	}
	if id != "app-a" {
		t.Fatalf("id = %q; want app-a", id)
	}
	if suffix != "/internal-routes/top-errors" {
		t.Fatalf("suffix = %q; want /internal-routes/top-errors", suffix)
	}
}

func TestServerStaticJSSkipsLicenseWhenDebugEnvEnabled(t *testing.T) {
	t.Setenv("NM_SKIP_LICENSE_CHECK", "true")
	server := New(Config{})

	req := httptest.NewRequest(http.MethodGet, "/static/main.js", nil)
	resp := httptest.NewRecorder()

	server.httpServer.Handler.ServeHTTP(resp, req)

	if resp.Code == http.StatusForbidden {
		t.Fatalf("status = %d body = %s; want license check skipped", resp.Code, resp.Body.String())
	}
}

func TestServerStaticJSRequiresLicenseByDefault(t *testing.T) {
	server := New(Config{Checker: &license.Checker{}})

	req := httptest.NewRequest(http.MethodGet, "/static/main.js", nil)
	resp := httptest.NewRecorder()

	server.httpServer.Handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s; want 403", resp.Code, resp.Body.String())
	}
}

func TestServerHandlesAppSLAConfig(t *testing.T) {
	store := &fakeConfigStore{}
	server := New(Config{ConfigStore: store, DefaultSLATarget: 0.999})

	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/apps/app-a/sla/config", strings.NewReader(`{"target":0.995}`))
	putReq.Header.Set("Content-Type", "application/json")
	putResp := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(putResp, putReq)
	if putResp.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body = %s; want 200", putResp.Code, putResp.Body.String())
	}
	if store.slaConfig.AppID != "app-a" || store.slaConfig.Target != 0.995 {
		t.Fatalf("stored sla config = %#v", store.slaConfig)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/apps/app-a/sla/config", nil)
	getResp := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("GET status = %d body = %s; want 200", getResp.Code, getResp.Body.String())
	}
	var body struct {
		Data model.SLAConfig `json:"data"`
	}
	if err := json.Unmarshal(getResp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.AppID != "app-a" || body.Data.Target != 0.995 {
		t.Fatalf("response data = %#v", body.Data)
	}
}

func TestServerHandlesAppRouteGroupRules(t *testing.T) {
	store := &fakeConfigStore{}
	server := New(Config{ConfigStore: store, DefaultSLATarget: 0.999})

	payload := `{"rules":[{"prefix":"/api/orders/","group":"/api/orders/*"}]}`
	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/apps/app-a/internal-routes/rules", strings.NewReader(payload))
	putReq.Header.Set("Content-Type", "application/json")
	putResp := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(putResp, putReq)
	if putResp.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body = %s; want 200", putResp.Code, putResp.Body.String())
	}
	if len(store.rules) != 1 || store.rules[0].Group != "/api/orders/*" {
		t.Fatalf("stored rules = %#v", store.rules)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/apps/app-a/internal-routes/rules", nil)
	getResp := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("GET status = %d body = %s; want 200", getResp.Code, getResp.Body.String())
	}
	var body struct {
		Data []model.RouteGroupRule `json:"data"`
	}
	if err := json.Unmarshal(getResp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].Prefix != "/api/orders/" {
		t.Fatalf("response data = %#v", body.Data)
	}
}

func TestServerHandlesAppHTTPLoggerSyncWithNamespaceAndRegionAppID(t *testing.T) {
	syncer := &fakeHTTPLoggerSyncer{}
	server := New(Config{HTTPLoggerSyncer: syncer})

	payload := `{"namespace":"team-ns","region_app_id":"region-app-a"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/12/gateway/http-logger/sync", strings.NewReader(payload))
	resp := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; want 200", resp.Code, resp.Body.String())
	}
	if syncer.namespace != "team-ns" {
		t.Fatalf("namespace = %q; want team-ns", syncer.namespace)
	}
	if syncer.matchAppID != "region-app-a" {
		t.Fatalf("matchAppID = %q; want region-app-a", syncer.matchAppID)
	}
	if syncer.mappingAppID != "12" {
		t.Fatalf("mappingAppID = %q; want 12", syncer.mappingAppID)
	}
	if !strings.Contains(resp.Body.String(), `"app_id":"12"`) || !strings.Contains(resp.Body.String(), `"region_app_id":"region-app-a"`) {
		t.Fatalf("response body = %s; want app_id 12 and region_app_id region-app-a", resp.Body.String())
	}
}

func TestServerRejectsHTTPLoggerSyncWithoutNamespace(t *testing.T) {
	server := New(Config{HTTPLoggerSyncer: &fakeHTTPLoggerSyncer{}})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/12/gateway/http-logger/sync", strings.NewReader(`{"region_app_id":"region-app-a"}`))
	resp := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s; want 400", resp.Code, resp.Body.String())
	}
}

type failingHTTPLoggerSyncer struct{}

func (failingHTTPLoggerSyncer) SyncHTTPLogger(_ context.Context, _, _ string) error {
	return fmt.Errorf("sync failed")
}

func TestServerIncludesRouteGroupSnapshotFreshnessMeta(t *testing.T) {
	server := New(Config{
		QueryStore: fakeRouteGroupQueryStore{
			items: []model.RouteGroupItem{{RouteGroup: "/api/orders/*", RequestCount: 3}},
			meta:  model.QueryMeta{Window: model.Window5m, FreshnessSeconds: 33, Stale: true},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/apps/app-a/internal-routes/top-errors?window=5m", nil)
	resp := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; want 200", resp.Code, resp.Body.String())
	}
	var body struct {
		Meta model.QueryMeta `json:"meta"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Meta.FreshnessSeconds != 33 || !body.Meta.Stale {
		t.Fatalf("meta = %#v; want freshness 33 stale true", body.Meta)
	}
}

func TestServerHandlesAppComponentSummary(t *testing.T) {
	server := New(Config{
		QueryStore: fakeRouteGroupQueryStore{
			components: []model.AppComponentSummary{{
				ComponentID:  "svc-a",
				ServiceAlias: "orders",
				Name:         "orders",
				RequestCount: 12,
				ErrorCount:   1,
				ErrorRate:    0.08333333333333333,
				AvgLatencyMs: 45,
			}},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/apps/app-a/components/summary?window=5m", nil)
	resp := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; want 200", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"component_id":"svc-a"`) || !strings.Contains(resp.Body.String(), `"avg_latency_ms":45`) {
		t.Fatalf("response body = %s; want component summary", resp.Body.String())
	}
}
