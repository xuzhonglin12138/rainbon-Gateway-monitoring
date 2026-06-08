package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type fakeGlobalRuleClient struct {
	upserts []globalRuleUpsert
	deletes []globalRuleDelete
}

type globalRuleUpsert struct {
	namespace string
	name      string
	config    HTTPLoggerConfig
}

type globalRuleDelete struct {
	namespaces []string
	name       string
}

func (f *fakeGlobalRuleClient) UpsertHTTPLoggerGlobalRule(_ context.Context, namespace, name string, cfg HTTPLoggerConfig) error {
	f.upserts = append(f.upserts, globalRuleUpsert{namespace: namespace, name: name, config: cfg})
	return nil
}

func (f *fakeGlobalRuleClient) DeleteManagedHTTPLoggerGlobalRules(_ context.Context, namespaces []string, name string) error {
	f.deletes = append(f.deletes, globalRuleDelete{namespaces: append([]string(nil), namespaces...), name: name})
	return nil
}

func TestBuildHTTPLoggerGlobalRuleIncludesManagedLabelsAndConfig(t *testing.T) {
	rule := BuildHTTPLoggerGlobalRule("team-ns", "rainbond-gateway-monitoring-http-logger", HTTPLoggerConfig{
		URI:       "http://collector",
		Timeout:   5,
		SSLVerify: true,
		LogFormat: map[string]string{"route_id": "$route_name"},
	})

	if rule.GetNamespace() != "team-ns" || rule.GetName() != "rainbond-gateway-monitoring-http-logger" {
		t.Fatalf("rule identity = %s/%s", rule.GetNamespace(), rule.GetName())
	}
	labels := rule.GetLabels()
	if labels[HTTPLoggerGlobalRuleManagedLabel] != "true" || labels["app.kubernetes.io/managed-by"] != "rainbond-gateway-monitoring" {
		t.Fatalf("labels = %#v; want managed labels", labels)
	}
	plugins, ok, err := unstructured.NestedSlice(rule.Object, "spec", "plugins")
	if err != nil || !ok || len(plugins) != 1 {
		t.Fatalf("plugins ok=%v err=%v len=%d; want one plugin", ok, err, len(plugins))
	}
	plugin := plugins[0].(map[string]interface{})
	if plugin["name"] != HTTPLoggerPluginName || plugin["enable"] != true {
		t.Fatalf("plugin = %#v; want enabled http-logger", plugin)
	}
	config := plugin["config"].(map[string]interface{})
	if config["uri"] != "http://collector" || config["timeout"] != int64(5) || config["ssl_verify"] != true {
		t.Fatalf("config = %#v; want collector config", config)
	}
	if _, ok := config["log_format"].(map[string]interface{}); !ok {
		t.Fatalf("log_format = %#v; want map", config["log_format"])
	}
}

func TestGlobalHTTPLoggerJobUpsertsRulesAndScansMappingsWhenReady(t *testing.T) {
	route := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"namespace": "team-ns",
			"name":      "gr1ea4bc-8080-demo",
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
	routeClient := &fakeRouteClient{routes: []*unstructured.Unstructured{route}}
	ruleClient := &fakeGlobalRuleClient{}
	store := &fakeRouteMappingStore{}
	job := GlobalHTTPLoggerJob{
		RouteClient:    routeClient,
		GlobalRules:    ruleClient,
		MappingStore:   store,
		Namespaces:     []string{""},
		GlobalRuleName: "rainbond-gateway-monitoring-http-logger",
		Config:         HTTPLoggerConfig{URI: "http://collector", Timeout: 3},
		Ready:          func() bool { return true },
	}

	if err := job.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() unexpected error: %v", err)
	}
	if len(ruleClient.upserts) != 1 {
		t.Fatalf("upserts = %#v; want one global rule upsert", ruleClient.upserts)
	}
	if ruleClient.upserts[0].namespace != "team-ns" {
		t.Fatalf("upsert namespace = %q; want team-ns", ruleClient.upserts[0].namespace)
	}
	if len(routeClient.updated) != 0 {
		t.Fatalf("route updates = %d; want zero in global mode", len(routeClient.updated))
	}
	if len(store.routeMappings) == 0 {
		t.Fatal("no route mappings saved")
	}
	if store.routeMappings[0].AppID != "region-app-a" {
		t.Fatalf("mapping = %#v; want region app id", store.routeMappings[0])
	}
}

func TestGlobalHTTPLoggerJobDeletesManagedRulesWhenNotReady(t *testing.T) {
	ruleClient := &fakeGlobalRuleClient{}
	job := GlobalHTTPLoggerJob{
		GlobalRules:    ruleClient,
		Namespaces:     []string{"team-a", "team-b"},
		GlobalRuleName: "rainbond-gateway-monitoring-http-logger",
		Ready:          func() bool { return false },
	}

	if err := job.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() unexpected error: %v", err)
	}
	if len(ruleClient.upserts) != 0 {
		t.Fatalf("upserts = %#v; want none when not ready", ruleClient.upserts)
	}
	if len(ruleClient.deletes) != 1 {
		t.Fatalf("deletes = %#v; want one delete call", ruleClient.deletes)
	}
	if ruleClient.deletes[0].namespaces[0] != "team-a" || ruleClient.deletes[0].namespaces[1] != "team-b" {
		t.Fatalf("delete namespaces = %#v", ruleClient.deletes[0].namespaces)
	}
}

func TestGlobalHTTPLoggerJobFiltersRoutesByIngressClass(t *testing.T) {
	matching := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"namespace": "team-a",
			"name":      "route-a",
			"labels":    map[string]interface{}{"creator": "Rainbond", "app_id": "app-a"},
		},
		"spec": map[string]interface{}{
			"ingressClassName": "apisix",
			"http":             []interface{}{map[string]interface{}{"name": "http-a"}},
		},
	}}
	other := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"namespace": "team-b",
			"name":      "route-b",
			"labels":    map[string]interface{}{"creator": "Rainbond", "app_id": "app-b"},
		},
		"spec": map[string]interface{}{
			"ingressClassName": "nginx",
			"http":             []interface{}{map[string]interface{}{"name": "http-b"}},
		},
	}}
	routeClient := &fakeRouteClient{routes: []*unstructured.Unstructured{matching, other}}
	ruleClient := &fakeGlobalRuleClient{}
	job := GlobalHTTPLoggerJob{
		RouteClient:      routeClient,
		GlobalRules:      ruleClient,
		Namespaces:       []string{""},
		GlobalRuleName:   "rainbond-gateway-monitoring-http-logger",
		IngressClassName: "apisix",
		Config:           HTTPLoggerConfig{URI: "http://collector", Timeout: 3},
		Ready:            func() bool { return true },
	}

	if err := job.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() unexpected error: %v", err)
	}
	if len(ruleClient.upserts) != 1 {
		t.Fatalf("upserts = %#v; want one matching ingress class", ruleClient.upserts)
	}
	if ruleClient.upserts[0].namespace != "team-a" {
		t.Fatalf("upsert namespace = %q; want team-a", ruleClient.upserts[0].namespace)
	}
}

func TestRouteMappingScanOnlyDoesNotUpdateApisixRoute(t *testing.T) {
	route := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":   "route-a",
			"labels": map[string]interface{}{"creator": "Rainbond", "app_id": "region-app-a"},
		},
		"spec": map[string]interface{}{
			"http": []interface{}{map[string]interface{}{"name": "http-a"}},
		},
	}}
	client := &fakeRouteClient{routes: []*unstructured.Unstructured{route}}
	store := &fakeRouteMappingStore{}
	job := HTTPLoggerAttachJob{
		Client:       client,
		MappingStore: store,
		Namespaces:   []string{"team-ns"},
		AppID:        "region-app-a",
		MappingOnly:  true,
		Config:       HTTPLoggerConfig{URI: "http://collector", Timeout: 3},
	}

	if err := job.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() unexpected error: %v", err)
	}
	if len(client.updated) != 0 {
		t.Fatalf("updates = %d; want none in mapping-only mode", len(client.updated))
	}
	if len(store.routeMappings) == 0 {
		t.Fatal("no route mappings were saved")
	}
	for _, mapping := range store.routeMappings {
		if mapping.AppID != "region-app-a" {
			t.Fatalf("mapping app id = %q; want region-app-a", mapping.AppID)
		}
	}
}

func TestGlobalHTTPLoggerJobStartUsesIntervalAndStops(t *testing.T) {
	ruleClient := &fakeGlobalRuleClient{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := GlobalHTTPLoggerJob{
		GlobalRules:    ruleClient,
		Namespaces:     []string{"team-ns"},
		GlobalRuleName: "rainbond-gateway-monitoring-http-logger",
		Interval:       time.Hour,
		Ready:          func() bool { return false },
	}
	job.Start(ctx)
	time.Sleep(20 * time.Millisecond)
	cancel()
	if len(ruleClient.deletes) == 0 {
		t.Fatal("expected initial cleanup run")
	}
}

var _ RouteMappingStore = (*fakeRouteMappingStore)(nil)
var _ AppPrometheusRouteReplacer = (*fakeRouteMappingStore)(nil)
var _ = model.RouteMapping{}
