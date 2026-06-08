package gateway

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestHTTPLoggerSyncerSyncsSingleNamespaceAndApp(t *testing.T) {
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
	syncer := HTTPLoggerSyncer{
		Client: client,
		Config: HTTPLoggerConfig{URI: "http://collector", Timeout: 3},
	}

	if err := syncer.SyncHTTPLogger(context.Background(), "tenant-ns", "region-app-a"); err != nil {
		t.Fatalf("SyncHTTPLogger() unexpected error: %v", err)
	}
	if len(client.updated) != 1 {
		t.Fatalf("updates = %d; want 1", len(client.updated))
	}
}

func TestHTTPLoggerSyncerMappingOnlyDoesNotUpdateRoute(t *testing.T) {
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
	syncer := HTTPLoggerSyncer{
		Client:       client,
		MappingStore: store,
		Config:       HTTPLoggerConfig{URI: "http://collector", Timeout: 3},
		MappingOnly:  true,
	}

	if err := syncer.SyncHTTPLogger(context.Background(), "tenant-ns", "region-app-a"); err != nil {
		t.Fatalf("SyncHTTPLogger() unexpected error: %v", err)
	}
	if len(client.updated) != 0 {
		t.Fatalf("updates = %d; want zero in mapping-only mode", len(client.updated))
	}
	if len(store.routeMappings) == 0 {
		t.Fatal("no route mappings saved")
	}
}
