package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type fakeRouteClient struct {
	routes  []*unstructured.Unstructured
	updated []*unstructured.Unstructured
}

type fakeRouteMappingStore struct {
	mappings       []string
	routeMappings  []model.RouteMapping
	replacedAppID  string
	replacedRoutes []string
}

func (f *fakeRouteClient) List(_ context.Context, _ string) ([]*unstructured.Unstructured, error) {
	return f.routes, nil
}

func (f *fakeRouteClient) Update(_ context.Context, _ string, route *unstructured.Unstructured) error {
	f.updated = append(f.updated, route)
	return nil
}

func (f *fakeRouteMappingStore) SaveRouteMapping(_ context.Context, mapping model.RouteMapping, _ time.Duration) error {
	f.mappings = append(f.mappings, mapping.AppID)
	f.routeMappings = append(f.routeMappings, mapping)
	return nil
}

func (f *fakeRouteMappingStore) ReplaceAppPrometheusRoutes(_ context.Context, appID string, routes []string) error {
	f.replacedAppID = appID
	f.replacedRoutes = append([]string(nil), routes...)
	return nil
}

func TestHTTPLoggerAttachJobRunOnceFiltersRoutesByAppID(t *testing.T) {
	matching := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":   "route-a",
			"labels": map[string]interface{}{"creator": "Rainbond", "app_id": "region-app-a"},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{map[string]interface{}{"name": "http-a"}},
		},
	}}
	other := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":   "route-b",
			"labels": map[string]interface{}{"creator": "Rainbond", "app_id": "region-app-b"},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{map[string]interface{}{"name": "http-b"}},
		},
	}}
	client := &fakeRouteClient{routes: []*unstructured.Unstructured{matching, other}}
	job := HTTPLoggerAttachJob{
		Client:     client,
		Namespaces: []string{"tenant-ns"},
		AppID:      "region-app-a",
		Config:     HTTPLoggerConfig{URI: "http://collector", Timeout: 3},
	}

	if err := job.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() unexpected error: %v", err)
	}
	if len(client.updated) != 1 {
		t.Fatalf("updates = %d; want 1", len(client.updated))
	}
	if client.updated[0].GetName() != "route-a" {
		t.Fatalf("updated route = %q; want route-a", client.updated[0].GetName())
	}
}

func TestHTTPLoggerAttachJobStoresConsoleAppIDWhenMatchingRegionAppID(t *testing.T) {
	matching := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":   "route-a",
			"labels": map[string]interface{}{"creator": "Rainbond", "app_id": "region-app-a"},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{map[string]interface{}{"name": "http-a"}},
		},
	}}
	client := &fakeRouteClient{routes: []*unstructured.Unstructured{matching}}
	store := &fakeRouteMappingStore{}
	job := HTTPLoggerAttachJob{
		Client:       client,
		MappingStore: store,
		Namespaces:   []string{"tenant-ns"},
		AppID:        "region-app-a",
		MappingAppID: "console-app-a",
		Config:       HTTPLoggerConfig{URI: "http://collector", Timeout: 3},
	}

	if err := job.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() unexpected error: %v", err)
	}
	if len(store.mappings) == 0 {
		t.Fatal("no route mappings were saved")
	}
	for _, appID := range store.mappings {
		if appID != "console-app-a" {
			t.Fatalf("saved mapping app_id = %q; want console-app-a", appID)
		}
	}
}

func TestHTTPLoggerAttachJobStoresDisplayMetadata(t *testing.T) {
	matching := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":   "route-a",
			"labels": map[string]interface{}{"creator": "Rainbond", "app_id": "region-app-a"},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{map[string]interface{}{"name": "http-a"}},
		},
	}}
	client := &fakeRouteClient{routes: []*unstructured.Unstructured{matching}}
	store := &fakeRouteMappingStore{}
	job := HTTPLoggerAttachJob{
		Client:       client,
		MappingStore: store,
		Namespaces:   []string{"tenant-ns"},
		AppID:        "region-app-a",
		MappingAppID: "console-app-a",
		Metadata: model.RouteMappingMetadata{
			RegionName:  "cn-east",
			RegionAppID: "region-app-a",
			TeamName:    "team-a",
			TeamAlias:   "研发团队",
			AppName:     "订单系统",
		},
		Config: HTTPLoggerConfig{URI: "http://collector", Timeout: 3},
	}

	if err := job.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() unexpected error: %v", err)
	}
	if len(store.routeMappings) == 0 {
		t.Fatal("no route mappings were saved")
	}
	mapping := store.routeMappings[0]
	if mapping.AppID != "console-app-a" || mapping.RegionAppID != "region-app-a" {
		t.Fatalf("mapping app ids = %#v; want console and region app ids", mapping)
	}
	if mapping.AppName != "订单系统" || mapping.TeamName != "team-a" || mapping.TeamAlias != "研发团队" || mapping.RegionName != "cn-east" {
		t.Fatalf("mapping display metadata = %#v; want app/team/region display metadata", mapping)
	}
}

func TestHTTPLoggerAttachJobMatchesRouteByServiceAliasAndStoresConsoleAppID(t *testing.T) {
	matching := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": "gr1ea4bc-8080-demo",
			"labels": map[string]interface{}{
				"creator":  "Rainbond",
				"app_id":   "region-app-a",
				"gr1ea4bc": "service_alias",
			},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{map[string]interface{}{"name": "http-a"}},
		},
	}}
	other := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":   "gr7bd8bd-8080-demo",
			"labels": map[string]interface{}{"creator": "Rainbond", "app_id": "region-app-b"},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{map[string]interface{}{"name": "http-b"}},
		},
	}}
	client := &fakeRouteClient{routes: []*unstructured.Unstructured{matching, other}}
	store := &fakeRouteMappingStore{}
	job := HTTPLoggerAttachJob{
		Client:         client,
		MappingStore:   store,
		Namespaces:     []string{"tenant-ns"},
		AppID:          "console-app-a",
		MappingAppID:   "console-app-a",
		ServiceAliases: []string{"gr1ea4bc"},
		Config:         HTTPLoggerConfig{URI: "http://collector", Timeout: 3},
	}

	if err := job.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() unexpected error: %v", err)
	}
	if len(client.updated) != 1 {
		t.Fatalf("updates = %d; want 1", len(client.updated))
	}
	if client.updated[0].GetName() != "gr1ea4bc-8080-demo" {
		t.Fatalf("updated route = %q; want gr1ea4bc-8080-demo", client.updated[0].GetName())
	}
	if len(store.mappings) == 0 {
		t.Fatal("no route mappings were saved")
	}
	for _, appID := range store.mappings {
		if appID != "console-app-a" {
			t.Fatalf("saved mapping app_id = %q; want console-app-a", appID)
		}
	}
}

func TestHTTPLoggerAttachJobReplacesAppPrometheusRoutesWithCurrentScan(t *testing.T) {
	matching := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": "gr1ea4bc-8080-demo",
			"labels": map[string]interface{}{
				"creator":  "Rainbond",
				"gr1ea4bc": "service_alias",
			},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{map[string]interface{}{"name": "http-a"}},
		},
	}}
	client := &fakeRouteClient{routes: []*unstructured.Unstructured{matching}}
	store := &fakeRouteMappingStore{}
	job := HTTPLoggerAttachJob{
		Client:         client,
		MappingStore:   store,
		Namespaces:     []string{"tenant-ns"},
		AppID:          "console-app-a",
		MappingAppID:   "console-app-a",
		ServiceAliases: []string{"gr1ea4bc"},
		Config:         HTTPLoggerConfig{URI: "http://collector", Timeout: 3},
	}

	if err := job.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() unexpected error: %v", err)
	}
	if store.replacedAppID != "console-app-a" {
		t.Fatalf("replaced app id = %q; want console-app-a", store.replacedAppID)
	}
	want := "tenant-ns_gr1ea4bc-8080-demo_http-a"
	if len(store.replacedRoutes) != 2 {
		t.Fatalf("replaced routes = %#v; want parent and child routes", store.replacedRoutes)
	}
	if store.replacedRoutes[1] != want {
		t.Fatalf("replaced child route = %q; want %q", store.replacedRoutes[1], want)
	}
}

func TestHTTPLoggerAttachJobClearsAppPrometheusRoutesWhenTargetAppHasNoMatches(t *testing.T) {
	other := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": "gr7bd8bd-8080-demo",
			"labels": map[string]interface{}{
				"creator":  "Rainbond",
				"gr7bd8bd": "service_alias",
			},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{map[string]interface{}{"name": "http-b"}},
		},
	}}
	client := &fakeRouteClient{routes: []*unstructured.Unstructured{other}}
	store := &fakeRouteMappingStore{}
	job := HTTPLoggerAttachJob{
		Client:         client,
		MappingStore:   store,
		Namespaces:     []string{"tenant-ns"},
		AppID:          "console-app-a",
		MappingAppID:   "console-app-a",
		ServiceAliases: []string{"gr1ea4bc"},
		Config:         HTTPLoggerConfig{URI: "http://collector", Timeout: 3},
	}

	if err := job.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() unexpected error: %v", err)
	}
	if store.replacedAppID != "console-app-a" {
		t.Fatalf("replaced app id = %q; want console-app-a", store.replacedAppID)
	}
	if len(store.replacedRoutes) != 0 {
		t.Fatalf("replaced routes = %#v; want empty route index", store.replacedRoutes)
	}
}

func TestHTTPLoggerAttachJobLogsRouteScanAndMappingSave(t *testing.T) {
	matching := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":   "route-a",
			"labels": map[string]interface{}{"creator": "Rainbond", "app_id": "region-app-a"},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{map[string]interface{}{"name": "http-a"}},
		},
	}}
	logger, hook := logtest.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)
	client := &fakeRouteClient{routes: []*unstructured.Unstructured{matching}}
	store := &fakeRouteMappingStore{}
	job := HTTPLoggerAttachJob{
		Client:       client,
		MappingStore: store,
		Namespaces:   []string{"tenant-ns"},
		AppID:        "region-app-a",
		MappingAppID: "console-app-a",
		Config:       HTTPLoggerConfig{URI: "http://collector", Timeout: 3},
		Logger:       logger,
	}

	if err := job.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() unexpected error: %v", err)
	}

	if len(hook.Entries) == 0 {
		t.Fatal("log entries length = 0; want diagnostic logs")
	}
	var sawScan, sawEnsure, sawMapping bool
	for _, entry := range hook.Entries {
		switch entry.Message {
		case "scanned apisix routes for http-logger":
			sawScan = sawScan || (entry.Data["namespace"] == "tenant-ns" && entry.Data["route_count"] == 1)
		case "ensured route-level http-logger":
			sawEnsure = sawEnsure || (entry.Data["collector_uri"] == "http://collector" && entry.Data["http_logger_timeout"] == 3 && entry.Data["http_logger_ssl_verify"] == false)
		case "saved apisix route mapping":
			sawMapping = sawMapping || (entry.Data["route_id"] == "route-a" && entry.Data["app_id"] == "console-app-a")
		}
	}
	if !sawScan {
		t.Fatalf("missing route scan log; entries=%#v", hook.Entries)
	}
	if !sawEnsure {
		t.Fatalf("missing http-logger config log; entries=%#v", hook.Entries)
	}
	if !sawMapping {
		t.Fatalf("missing route mapping log; entries=%#v", hook.Entries)
	}
}
