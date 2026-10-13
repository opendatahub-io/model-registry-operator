package catalogcontent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func fixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	bench := filepath.Join(root, "benchmarks")
	if err := os.Mkdir(bench, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "sources.yaml"), `catalogs:
- id: models
  type: yaml
  properties: {yamlCatalogPath: models.yaml}
mcp_catalogs:
- id: mcp
  type: yaml
  properties: {yamlCatalogPath: mcp.yaml}
agent_catalogs:
- id: agents
  type: yaml
  properties: {yamlCatalogPath: agents.yaml}
serving_runtime_catalogs:
- id: runtimes
  type: yaml
  properties: {yamlCatalogPath: runtimes.yaml}
`)
	writeFile(t, filepath.Join(root, "models.yaml"), "models:\n- name: example\n  artifacts: [{uri: 'oci://registry/model:v1'}]\n")
	writeFile(t, filepath.Join(root, "mcp.yaml"), "mcp_servers:\n- name: example\n  version: v1\n  endpoints: {http: 'https://example.com'}\n  tools: [{name: search}]\n- name: example\n  version: v2\n")
	writeFile(t, filepath.Join(root, "agents.yaml"), "agents: [{name: example}]\n")
	writeFile(t, filepath.Join(root, "runtimes.yaml"), "serving_runtimes:\n- name: example\n  versions: [{version: v1, image: 'registry/runtime:v1'}]\n")
	writeFile(t, filepath.Join(bench, "metadata.json"), `{"id":"model"}`)
	writeFile(t, filepath.Join(bench, "performance.ndjson"), `{"model_id":"model","config_id":"config"}`)
	return root, bench
}
func TestValidateIndependentContent(t *testing.T) {
	root, bench := fixture(t)
	if failure := Validate(filepath.Join(root, "sources.yaml"), bench); failure != nil {
		t.Fatal(failure)
	}
	// Optional administrator content is not part of shipped validation.
	writeFile(t, filepath.Join(root, "user-sources.yaml"), "[broken")
	if failure := Validate(filepath.Join(root, "sources.yaml"), bench); failure != nil {
		t.Fatal(failure)
	}
}

func TestModelArtifactTypes(t *testing.T) {
	for _, tc := range []struct{ name, artifact, errorText string }{
		{"omitted type", "{uri: 'oci://registry/model:v1'}", ""},
		{"empty type", "{artifactType: '', uri: 'oci://registry/model:v1'}", ""},
		{"null type", "{artifactType: null, uri: 'oci://registry/model:v1'}", ""},
		{"model artifact", "{artifactType: model-artifact, uri: 'oci://registry/model:v1'}", ""},
		{"metrics artifact without URI", "{artifactType: metrics-artifact}", ""},
		{"unknown type with valid URI", "{artifactType: bogus, uri: 'oci://registry/model:v1'}", `unknown artifactType "bogus"`},
		{"numeric type", "{artifactType: 42, uri: 'oci://registry/model:v1'}", "artifactType must be a string"},
		{"boolean type", "{artifactType: true, uri: 'oci://registry/model:v1'}", "artifactType must be a string"},
		{"list type", "{artifactType: [model-artifact], uri: 'oci://registry/model:v1'}", "artifactType must be a string"},
		{"object type", "{artifactType: {}, uri: 'oci://registry/model:v1'}", "artifactType must be a string"},
		{"model artifact still requires valid URI", "{artifactType: model-artifact, uri: broken}", "invalid artifact URI"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bench := fixture(t)
			writeFile(t, filepath.Join(root, "models.yaml"), "models:\n- name: example\n  artifacts: ["+tc.artifact+"]\n")
			failure := Validate(filepath.Join(root, "sources.yaml"), bench)
			if tc.errorText == "" {
				if failure != nil {
					t.Fatal(failure)
				}
				return
			}
			if failure == nil || failure.Stage != "Loading" || failure.Reason != "CatalogContentInvalid" ||
				!strings.Contains(failure.Message, tc.errorText) || !strings.Contains(failure.Message, "source models, models[0]") {
				t.Fatalf("failure=%v, want CatalogContentInvalid identifying the source/model and %q", failure, tc.errorText)
			}
		})
	}
}

func TestRejectMalformedShippedContent(t *testing.T) {
	for _, tc := range []struct{ name, file, body, reason string }{
		{"multiple documents", "models.yaml", "models: []\n---\nmodels: [null]\n", "CatalogContentInvalid"},
		{"duplicate YAML key", "models.yaml", "models: []\nmodels: []\n", "CatalogContentInvalid"},
		{"syntax", "models.yaml", "models: [", "CatalogContentInvalid"},
		{"missing list", "models.yaml", "unknown: []", "CatalogContentInvalid"},
		{"null model", "models.yaml", "models: [null]", "CatalogContentInvalid"},
		{"duplicate model", "models.yaml", "models: [{name: x}, {name: x}]", "CatalogContentInvalid"},
		{"invalid uri", "models.yaml", "models: [{name: x, artifacts: [{uri: broken}]}]", "CatalogContentInvalid"},
		{"null artifact", "models.yaml", "models: [{name: x, artifacts: [null]}]", "CatalogContentInvalid"},
		{"null MCP tool", "mcp.yaml", "mcp_servers: [{name: x, tools: [null]}]", "CatalogContentInvalid"},
		{"invalid endpoint", "mcp.yaml", "mcp_servers: [{name: x, endpoints: {http: nope}}]", "CatalogContentInvalid"},
		{"duplicate MCP identity", "mcp.yaml", "mcp_servers: [{name: x, version: v1}, {name: ' x ', version: v1}]", "CatalogContentInvalid"},
		{"empty agent name", "agents.yaml", "agents: [{name: ' '}]", "CatalogContentInvalid"},
		{"runtime version", "runtimes.yaml", "serving_runtimes: [{name: x, versions: [{image: test}]}]", "CatalogContentInvalid"},
		{"invalid metadata", "benchmarks/metadata.json", `{"id":4}`, "BenchmarkContentInvalid"},
		{"invalid NDJSON", "benchmarks/performance.ndjson", "{broken", "BenchmarkContentInvalid"},
		{"null metric", "benchmarks/performance.ndjson", "null", "BenchmarkContentInvalid"},
		{"array metric", "benchmarks/performance.ndjson", "[]", "BenchmarkContentInvalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bench := fixture(t)
			writeFile(t, filepath.Join(root, tc.file), tc.body)
			failure := Validate(filepath.Join(root, "sources.yaml"), bench)
			if failure == nil || failure.Reason != tc.reason {
				t.Fatalf("failure=%v, want %s", failure, tc.reason)
			}
		})
	}
}

func TestPublishedBenchmarkRecordFormats(t *testing.T) {
	// Records from quay.io/opendatahub/odh-model-metadata-collection:latest
	// (sha256:7fd50e85b668d54a20ea2b2fe6034bf059c3d5868cdf476a82ece346cb1b0bf9).
	root, bench := fixture(t)
	for _, model := range []struct {
		dir, id, evaluations, performance string
	}{
		{
			"testcorp/sample-model-1.0", "testcorp/sample-model",
			`{"id": 1, "overall_average": 3.25, "arc_v1": 3.40, "mmlu_v1": 3.75, "gsm8k_v1": 2.60, "accelerator_type": "H100", "model_hf_repo_name": "testcorp/sample-model"}`,
			`{"config_id": "abc123def456", "ttft_mean": 145.32, "ttft_p90": 168.75, "e2e_mean": 6850.42, "tps_mean": 3920.18, "hardware": "H100", "framework": "pytorch", "framework_version": "2.0.1", "docker_image": "testcorp/inference:2.0.1", "model_hf_repo_name": "testcorp/sample-model"}`,
		},
		{
			"acmetech/text-analyzer-2.1", "acmetech/text-analyzer",
			`{"metric": "accuracy", "value": 0.924, "dataset": "synthetic-text-v1", "evaluation_date": "2025-01-15T14:30:00Z"}`,
			`{"config_id": "xyz789abc123", "ttft_mean": 98.65, "ttft_p90": 112.34, "e2e_mean": 4230.18, "tps_mean": 5847.92, "hardware": "V100", "framework": "tensorflow", "framework_version": "2.8.0", "docker_image": "acmetech/inference:2.8.0", "model_hf_repo_name": "acmetech/text-analyzer"}`,
		},
	} {
		dir := filepath.Join(bench, model.dir)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "metadata.json"), `{"id":"`+model.id+`"}`)
		writeFile(t, filepath.Join(dir, "evaluations.ndjson"), model.evaluations)
		writeFile(t, filepath.Join(dir, "performance.ndjson"), model.performance)
	}
	if failure := Validate(filepath.Join(root, "sources.yaml"), bench); failure != nil {
		t.Fatalf("published benchmark records were rejected: %+v", failure)
	}
}
func TestMissingFilesAndDisabledSources(t *testing.T) {
	root, bench := fixture(t)
	path := filepath.Join(root, "sources.yaml")
	writeFile(t, path, "catalogs:\n- id: missing\n  type: yaml\n  properties: {yamlCatalogPath: absent.yaml}\n")
	if failure := Validate(path, bench); failure == nil {
		t.Fatal("accepted missing required file")
	}
	writeFile(t, path, "catalogs:\n- id: missing\n  enabled: false\n- id: models\n  type: yaml\n  properties: {yamlCatalogPath: models.yaml}\n")
	if failure := Validate(path, bench); failure != nil {
		t.Fatal(failure)
	}
}
func TestOptionalBenchmarkData(t *testing.T) {
	root, _ := fixture(t)
	bench := t.TempDir()
	sources := filepath.Join(root, "sources.yaml")
	if failure := Validate(sources, bench); failure != nil {
		t.Fatalf("rejected image without benchmark data: %+v", failure)
	}
	model := filepath.Join(bench, "model-without-metrics")
	if err := os.Mkdir(model, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(model, "performance.ndjson"), `{}`)
	if failure := Validate(sources, bench); failure == nil || failure.Reason != "BenchmarkContentInvalid" {
		t.Fatalf("accepted benchmark records without adjacent metadata: %+v", failure)
	}
	if err := os.Remove(filepath.Join(model, "performance.ndjson")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(model, "metadata.json"), `{"id":"model-without-metrics"}`)
	if failure := Validate(sources, bench); failure != nil {
		t.Fatalf("rejected model without metric records: %+v", failure)
	}
}
func TestTerminationMessageIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination")
	failure := &Failure{"Loading", "CatalogContentInvalid", "https://user:secret@example.com/model?token=secret" + strings.Repeat("\x00", 2000)}
	if err := failure.Write(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 4096 || !json.Valid(data) {
		t.Fatal("invalid termination message")
	}
	if strings.Contains(string(data), "secret") || strings.Contains(failure.Error(), "secret") {
		t.Fatal("raw validation detail leaked into termination status or process error")
	}
}

func TestBenchmarkMetadataReadIsBounded(t *testing.T) {
	root, bench := fixture(t)
	if err := os.Truncate(filepath.Join(bench, "metadata.json"), maxYAMLBytes+1); err != nil {
		t.Fatal(err)
	}
	failure := Validate(filepath.Join(root, "sources.yaml"), bench)
	if failure == nil || failure.Reason != "BenchmarkContentInvalid" || !strings.Contains(failure.Message, "exceeds") {
		t.Fatalf("oversized metadata was not rejected: %+v", failure)
	}
}

func TestBenchmarkNDJSONRecordLimit(t *testing.T) {
	root, bench := fixture(t)
	path := filepath.Join(bench, "performance.ndjson")
	writeFile(t, path, `{"model_id":"model","config_id":"config","details":"`+strings.Repeat("x", 70<<10)+`"}`)
	if failure := Validate(filepath.Join(root, "sources.yaml"), bench); failure != nil {
		t.Fatalf("valid record larger than 64 KiB was rejected: %+v", failure)
	}
	if err := os.Truncate(path, maxNDJSONRecordBytes+1); err != nil {
		t.Fatal(err)
	}
	if failure := Validate(filepath.Join(root, "sources.yaml"), bench); failure == nil || failure.Reason != "BenchmarkContentInvalid" {
		t.Fatalf("oversized record was accepted: %+v", failure)
	}
}
