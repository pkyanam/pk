package routing

import (
	"fmt"
	"testing"
)

func TestChoose(t *testing.T) {
	rules := []Rule{
		{Name: "root", Prefix: "/", Priority: -1, Enabled: true},
		{Name: "api", Prefix: "/api", Priority: 1, Enabled: true},
		{Name: "v2", Prefix: "/api/v2", Priority: 0, Enabled: true},
		{Name: "private", Prefix: "/api/v2/private", Priority: 100, Enabled: false},
	}
	for i := 0; i < 72; i++ {
		path, want := fmt.Sprintf("/docs/pages/%03d", i), "root"
		if i >= 24 && i < 48 {
			path, want = fmt.Sprintf("/api/items/%03d", i-24), "api"
		}
		if i >= 48 {
			path, want = fmt.Sprintf("/api/v2/items/%03d", i-48), "v2"
		}
		t.Run(fmt.Sprintf("route-%03d", i), func(t *testing.T) {
			got, ok := Choose(rules, path)
			if !ok || got.Name != want {
				t.Fatalf("Choose(%q) = %#v, %t; want name=%q", path, got, ok, want)
			}
		})
	}
	t.Run("stable-tie-and-no-mutation", func(t *testing.T) {
		input := []Rule{{Name: "first", Prefix: "/same", Priority: 4, Enabled: true}, {Name: "second", Prefix: "/same", Priority: 4, Enabled: true}}
		got, ok := Choose(input, "/same/item")
		if !ok || got.Name != "first" {
			t.Fatalf("tie choice = %#v, %t; want first", got, ok)
		}
		if input[0].Name != "first" || input[1].Name != "second" {
			t.Fatal("Choose mutated rules")
		}
	})
	t.Run("segment-boundary-empty-prefix-and-relative-path", func(t *testing.T) {
		input := []Rule{{Name: "empty", Prefix: "", Enabled: true}, {Name: "api", Prefix: "/api", Enabled: true}, {Name: "root", Prefix: "/", Enabled: true}}
		for _, test := range []struct{ path, want string }{{"/apix", "root"}, {"relative", ""}, {"/api/", "api"}} {
			got, ok := Choose(input, test.path)
			if (test.want != "") != ok || (ok && got.Name != test.want) {
				t.Fatalf("Choose(%q) = %#v, %t; want %q", test.path, got, ok, test.want)
			}
		}
	})
}
