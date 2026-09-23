package catalog

import "testing"

func TestSelect(t *testing.T) {
	sources := []Source{
		{Name: "fallback", Prefix: "/", Enabled: true},
		{Name: "api", Prefix: "/api", Priority: 1, Enabled: true},
		{Name: "v2", Prefix: "/api/v2", Enabled: true},
		{Name: "disabled", Prefix: "/api/v2/private", Priority: 9},
	}
	got, ok := Select(sources, "/api/v2/items")
	if !ok || got.Name != "v2" {
		t.Fatalf("Select() = %#v, %t; want v2", got, ok)
	}
}
