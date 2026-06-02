package service

import "testing"

func TestRouteGroupResolverNormalizesHighCardinalityPaths(t *testing.T) {
	resolver := NewRouteGroupResolver(RouteGroupConfig{
		MaxGroupsPerScope: 100,
		TemplateRules: []RouteGroupRule{
			{Prefix: "/api/user/setting/", Group: "/api/user/setting/*"},
			{Prefix: "/api/order/detail/", Group: "/api/order/detail/*"},
		},
	})

	tests := []struct {
		name string
		uri  string
		want string
	}{
		{name: "template user setting", uri: "/api/user/setting/profile?tab=base", want: "/api/user/setting/*"},
		{name: "template order detail", uri: "/api/order/detail/123", want: "/api/order/detail/*"},
		{name: "numeric id", uri: "/v1/users/42/orders/9001", want: "/v1/users/{id}/orders/{id}"},
		{name: "uuid", uri: "/api/items/550e8400-e29b-41d4-a716-446655440000", want: "/api/items/{uuid}"},
		{name: "date", uri: "/reports/2026-06-02/export", want: "/reports/{date}/export"},
		{name: "long hash", uri: "/assets/0123456789abcdef0123456789abcdef", want: "/assets/{hash}"},
		{name: "empty path", uri: "", want: "/other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolver.Resolve(RouteGroupInput{AppID: "app-1", ComponentID: "svc-1", URI: tt.uri})
			if got != tt.want {
				t.Fatalf("Resolve(%q) = %q; want %q", tt.uri, got, tt.want)
			}
		})
	}
}
