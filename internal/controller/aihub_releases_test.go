package controller

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadComponentReleases_ParsesBothFiles(t *testing.T) {
	dir := t.TempDir()
	writeMetadata(t, dir, "modelregistry", `releases:
  - name: ComponentA
    version: v0.0.1
    repoUrl: https://example.com/a
  - name: ComponentB
    version: v0.0.2
    repoUrl: https://example.com/b
`)
	writeMetadata(t, dir, "catalog", `releases:
  - name: ComponentA
    version: v0.0.3
    repoUrl: https://example.com/a
  - name: ComponentC
    version: v0.0.4
    repoUrl: https://example.com/c
`)

	releases, err := loadComponentReleases(dir, []string{"modelregistry", "catalog"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(releases) != 3 {
		t.Fatalf("expected 3 releases, got %d", len(releases))
	}
	if releases[0].Name != "ComponentA" || releases[0].Version != "v0.0.1" {
		t.Errorf("unexpected first release: %+v", releases[0])
	}
	if releases[1].Name != "ComponentB" {
		t.Errorf("unexpected second release: %+v", releases[1])
	}
	if releases[2].Name != "ComponentC" {
		t.Errorf("unexpected third release: %+v", releases[2])
	}
}

func TestLoadComponentReleases_MissingFile(t *testing.T) {
	dir := t.TempDir()
	writeMetadata(t, dir, "modelregistry", `releases:
  - name: ComponentA
    version: v0.0.1
    repoUrl: https://example.com/a
`)

	releases, err := loadComponentReleases(dir, []string{"modelregistry", "nonexistent"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("expected 1 release, got %d", len(releases))
	}
}

func TestLoadComponentReleases_Fallback(t *testing.T) {
	dir := t.TempDir()

	releases, err := loadComponentReleases(dir, []string{"modelregistry", "catalog"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("expected 1 fallback release, got %d", len(releases))
	}
	if releases[0].Name != "model-registry-operator" {
		t.Errorf("unexpected fallback name: %s", releases[0].Name)
	}
	if releases[0].Version != "unknown" {
		t.Errorf("unexpected fallback version: %s", releases[0].Version)
	}
	if releases[0].RepoURL != "https://github.com/opendatahub-io/model-registry-operator" {
		t.Errorf("unexpected fallback repoURL: %s", releases[0].RepoURL)
	}
}

func TestLoadComponentReleases_DefaultEmptyVersion(t *testing.T) {
	dir := t.TempDir()
	writeMetadata(t, dir, "modelregistry", `releases:
  - name: ComponentA
    repoUrl: https://example.com/a
`)

	releases, err := loadComponentReleases(dir, []string{"modelregistry"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("expected 1 release, got %d", len(releases))
	}
	if releases[0].Version != "unknown" {
		t.Errorf("expected empty version to default to 'unknown', got %q", releases[0].Version)
	}
}

func writeMetadata(t *testing.T, base, component, content string) {
	t.Helper()
	dir := filepath.Join(base, component)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "component_metadata.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
