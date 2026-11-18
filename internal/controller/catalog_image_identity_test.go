package controller

import (
	"strings"
	"testing"
)

func TestSameCatalogImageReference(t *testing.T) {
	d1 := "sha256:" + strings.Repeat("1", 64)
	d2 := "sha256:" + strings.Repeat("2", 64)
	for _, tc := range []struct {
		name, actual, desired string
		want                  bool
	}{
		{"same pinned reference", "registry.example/a@" + d1, "registry.example/a@" + d1, true},
		{"mirrored digest", "mirror.example/a@" + d1, "registry.example/a@" + d1, true},
		{"different repository same content", "mirror.example/b@" + d1, "registry.example/a@" + d1, true},
		{"different content", "mirror.example/a@" + d2, "registry.example/a@" + d1, false},
		{"same tag", "registry.example/a:stable", "registry.example/a:stable", true},
		{"tag is not content identity", "mirror.example/a:stable", "registry.example/a:stable", false},
		{"different tags", "registry.example/a:new", "registry.example/a:old", false},
		{"tag and digest", "registry.example/a:stable", "registry.example/a@" + d1, false},
		{"malformed digest", "mirror.example/a@sha256:1", "registry.example/a@" + d1, false},
		{"empty references", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameCatalogImageReference(tc.actual, tc.desired); got != tc.want {
				t.Fatalf("sameCatalogImageReference(%q, %q)=%t, want %t", tc.actual, tc.desired, got, tc.want)
			}
		})
	}
}
