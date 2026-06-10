package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/license"
	"github.com/goodrain/rainbond-plugin-template/pkg/model"
	"github.com/goodrain/rainbond-plugin-template/pkg/service"
	logtest "github.com/sirupsen/logrus/hooks/test"
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
	apps       []model.AppTrafficItem
	components []model.AppComponentSummary
	meta       model.QueryMeta
	atEndTime  time.Time
	atCalled   bool
}

func (f fakeRouteGroupQueryStore) ListRouteGroups(_ context.Context, _ model.AggregateScope, _ model.Window, _ int, _ string) ([]model.RouteGroupItem, error) {
	return f.items, nil
}

func (f fakeRouteGroupQueryStore) ListApps(_ context.Context, _ model.AggregateScope, _ model.Window, _ int, _ string) ([]model.AppTrafficItem, error) {
	return f.apps, nil
}

func (f *fakeRouteGroupQueryStore) ListAppsAt(_ context.Context, _ model.AggregateScope, _ model.Window, endTime time.Time, _ int, _ string) ([]model.AppTrafficItem, error) {
	f.atCalled = true
	f.atEndTime = endTime
	return f.apps, nil
}

func (f fakeRouteGroupQueryStore) GetRouteGroupSnapshotMeta(_ context.Context, _ model.AggregateScope, _ model.Window, _ string) (model.QueryMeta, error) {
	return f.meta, nil
}

func (f fakeRouteGroupQueryStore) GetAppTrafficSnapshotMeta(_ context.Context, _ model.AggregateScope, _ model.Window, _ string) (model.QueryMeta, error) {
	return f.meta, nil
}

func (f fakeRouteGroupQueryStore) ListAppComponentSummaries(_ context.Context, _ string, _ model.Window, _ int) ([]model.AppComponentSummary, error) {
	return f.components, nil
}

type fakeHTTPLoggerSyncer struct {
	namespace      string
	appID          string
	matchAppID     string
	mappingAppID   string
	metadata       model.RouteMappingMetadata
	serviceAliases []string
}

type collectorContextStore struct {
	canceled bool
}

func (s *collectorContextStore) AddRouteGroupBucket(ctx context.Context, _ model.AggregateScope, _ model.Window, _ int64, _ model.RouteGroupMetric) error {
	s.canceled = ctx.Err() != nil
	return nil
}

type collectorContextMapper struct{}

func (collectorContextMapper) ResolveRoute(_ context.Context, routeID, serviceID string) (model.RouteMapping, error) {
	return model.RouteMapping{RouteID: routeID, AppID: "app-a", ComponentID: serviceID}, nil
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

func (f *fakeHTTPLoggerSyncer) SyncHTTPLoggerForAppRoutes(_ context.Context, namespace, matchAppID, mappingAppID string, serviceAliases []string) error {
	f.namespace = namespace
	f.matchAppID = matchAppID
	f.mappingAppID = mappingAppID
	f.serviceAliases = serviceAliases
	return nil
}

func (f *fakeHTTPLoggerSyncer) SyncHTTPLoggerForAppRoutesWithMetadata(_ context.Context, namespace, matchAppID, mappingAppID string, serviceAliases []string, metadata model.RouteMappingMetadata) error {
	f.namespace = namespace
	f.matchAppID = matchAppID
	f.mappingAppID = mappingAppID
	f.serviceAliases = serviceAliases
	f.metadata = metadata
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

func TestCollectApisixLogsDoesNotUseCanceledRequestContextForWrites(t *testing.T) {
	t.Setenv("NM_SKIP_LICENSE_CHECK", "true")
	store := &collectorContextStore{}
	collector := service.NewInternalRouteCollector(service.CollectorConfig{
		Store:  store,
		Mapper: collectorContextMapper{},
	})
	server := New(Config{Collector: collector})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/collector/apisix/logs", strings.NewReader(`[{"route_id":"route-a","service_id":"svc-a","uri":"/api/ping","status":200}]`)).WithContext(ctx)
	resp := httptest.NewRecorder()

	server.httpServer.Handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s; want 202", resp.Code, resp.Body.String())
	}
	if store.canceled {
		t.Fatal("collector write context was canceled by request context")
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

func TestServerHandlesAppHTTPLoggerSyncWithServiceAliases(t *testing.T) {
	syncer := &fakeHTTPLoggerSyncer{}
	server := New(Config{HTTPLoggerSyncer: syncer})

	payload := `{"namespace":"team-ns","region_app_id":"region-app-a","region_name":"cn-east","team_name":"team-a","team_alias":"研发团队","app_name":"订单系统","service_aliases":["gr1ea4bc"," gr1ea4bc ","gr7bd8bd"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/12/gateway/http-logger/sync", strings.NewReader(payload))
	resp := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; want 200", resp.Code, resp.Body.String())
	}
	if len(syncer.serviceAliases) != 2 {
		t.Fatalf("serviceAliases = %#v; want two deduplicated aliases", syncer.serviceAliases)
	}
	if syncer.serviceAliases[0] != "gr1ea4bc" || syncer.serviceAliases[1] != "gr7bd8bd" {
		t.Fatalf("serviceAliases = %#v; want gr1ea4bc, gr7bd8bd", syncer.serviceAliases)
	}
	if syncer.metadata.AppName != "订单系统" || syncer.metadata.TeamAlias != "研发团队" || syncer.metadata.RegionName != "cn-east" {
		t.Fatalf("metadata = %#v; want app/team/region display metadata", syncer.metadata)
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"service_aliases":["gr1ea4bc","gr7bd8bd"]`) {
		t.Fatalf("response body = %s; want normalized service_aliases", body)
	}
	if !strings.Contains(body, `"app_name":"订单系统"`) || !strings.Contains(body, `"team_alias":"研发团队"`) || !strings.Contains(body, `"region_name":"cn-east"`) {
		t.Fatalf("response body = %s; want display metadata", body)
	}
}

func TestServerRefreshesRouteMappingsForGlobalHTTPLoggerMode(t *testing.T) {
	syncer := &fakeHTTPLoggerSyncer{}
	server := New(Config{HTTPLoggerMode: "global", HTTPLoggerSyncer: syncer})

	payload := `{"namespace":"team-ns","region_app_id":"region-app-a","app_name":"订单系统","service_aliases":["gr1ea4bc"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/12/gateway/http-logger/sync", strings.NewReader(payload))
	resp := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; want 200", resp.Code, resp.Body.String())
	}
	if syncer.namespace != "team-ns" || syncer.matchAppID != "region-app-a" || syncer.mappingAppID != "12" {
		t.Fatalf("syncer call = %#v; want global mode to refresh console app mapping", syncer)
	}
	if len(syncer.serviceAliases) != 1 || syncer.serviceAliases[0] != "gr1ea4bc" {
		t.Fatalf("service aliases = %#v; want gr1ea4bc", syncer.serviceAliases)
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"mode":"global"`) || !strings.Contains(body, `"synced":true`) {
		t.Fatalf("response body = %s; want global mapping refresh response", body)
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

func TestServerLogsRouteGroupTopResultDiagnostics(t *testing.T) {
	logger, hook := logtest.NewNullLogger()
	server := New(Config{
		Logger: logger,
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

	for _, entry := range hook.Entries {
		if entry.Message != "listed route group top" {
			continue
		}
		if entry.Data["item_count"] != 1 {
			t.Fatalf("item_count = %v; want 1", entry.Data["item_count"])
		}
		if entry.Data["stale"] != true {
			t.Fatalf("stale = %v; want true", entry.Data["stale"])
		}
		if entry.Data["freshness_seconds"] != int64(33) {
			t.Fatalf("freshness_seconds = %v; want 33", entry.Data["freshness_seconds"])
		}
		return
	}
	t.Fatalf("missing route group result log; entries=%#v", hook.Entries)
}

func TestServerHandlesPlatformAppTopThroughput(t *testing.T) {
	server := New(Config{
		QueryStore: fakeRouteGroupQueryStore{
			apps: []model.AppTrafficItem{{
				AppID:               "app-a",
				TeamID:              "team-a",
				RequestCount:        600,
				ErrorCount:          2,
				ErrorRate:           float64(2) / float64(600),
				AvgLatencyMs:        45,
				ThroughputPerSecond: 2,
			}},
			meta: model.QueryMeta{Window: model.Window30m, FreshnessSeconds: 9},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/apps/top-throughput?window=30m", nil)
	resp := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; want 200", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"app_id":"app-a"`) || !strings.Contains(resp.Body.String(), `"throughput_per_second":2`) {
		t.Fatalf("response body = %s; want app throughput ranking", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"window":"30m"`) || !strings.Contains(resp.Body.String(), `"freshness_seconds":9`) {
		t.Fatalf("response body = %s; want selected window meta", resp.Body.String())
	}
}

func TestServerPassesEndTimeToPlatformAppTop(t *testing.T) {
	store := &fakeRouteGroupQueryStore{
		apps: []model.AppTrafficItem{{
			AppID:        "app-a",
			RequestCount: 70,
		}},
		meta: model.QueryMeta{Window: model.Window5m, FreshnessSeconds: 33, Stale: true},
	}
	server := New(Config{QueryStore: store})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/platform/apps/top-throughput?window=5m&end_time=1710000000", nil)
	resp := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; want 200", resp.Code, resp.Body.String())
	}
	if !store.atCalled {
		t.Fatalf("ListAppsAt was not called")
	}
	if got := store.atEndTime.Unix(); got != 1710000000 {
		t.Fatalf("end time = %d; want 1710000000", got)
	}
	if strings.Contains(resp.Body.String(), `"freshness_seconds":33`) || strings.Contains(resp.Body.String(), `"stale":true`) {
		t.Fatalf("response body = %s; want explicit end_time query to bypass latest snapshot meta", resp.Body.String())
	}
}

func TestServerHandlesTeamAppTopErrors(t *testing.T) {
	server := New(Config{
		QueryStore: fakeRouteGroupQueryStore{
			apps: []model.AppTrafficItem{{
				AppID:        "app-a",
				TeamID:       "team-a",
				RequestCount: 12,
				ErrorCount:   3,
				ErrorRate:    0.25,
				AvgLatencyMs: 80,
			}},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/teams/team-a/apps/top-errors?window=5m", nil)
	resp := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s; want 200", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"team_id":"team-a"`) || !strings.Contains(resp.Body.String(), `"error_count":3`) {
		t.Fatalf("response body = %s; want team app error ranking", resp.Body.String())
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
