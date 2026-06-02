package gateway

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type fakeRouteClient struct {
	routes  []*unstructured.Unstructured
	updated []*unstructured.Unstructured
}

func (f *fakeRouteClient) List(_ context.Context, _ string) ([]*unstructured.Unstructured, error) {
	return f.routes, nil
}

func (f *fakeRouteClient) Update(_ context.Context, _ string, route *unstructured.Unstructured) error {
	f.updated = append(f.updated, route)
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
