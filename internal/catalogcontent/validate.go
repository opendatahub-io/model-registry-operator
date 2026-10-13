// Package catalogcontent validates the operator's shipped data contract without
// connecting to a database or importing catalog server implementation packages.
package catalogcontent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

type Failure struct {
	Stage   string `json:"stage"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func (f *Failure) Error() string { return f.Stage + ": " + f.Reason + ": " + f.publicMessage() }

// Shipped content and container output can contain credentials. Keep detailed
// validation errors in process memory, not in pod logs or termination status.
func (f *Failure) publicMessage() string {
	if f.Reason == "BenchmarkContentInvalid" {
		return "Shipped benchmark content failed validation"
	}
	return "Shipped catalog content failed validation"
}

func (f *Failure) Write(path string) error {
	bounded := *f
	bounded.Message = f.publicMessage()
	data, err := json.Marshal(bounded)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

type Source struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Enabled    *bool  `json:"enabled"`
	Properties struct {
		Path string `json:"yamlCatalogPath"`
	} `json:"properties"`
}
type Config struct {
	Models       []Source `json:"model_catalogs"`
	LegacyModels []Source `json:"catalogs"`
	MCP          []Source `json:"mcp_catalogs"`
	Agents       []Source `json:"agent_catalogs"`
	Runtimes     []Source `json:"serving_runtime_catalogs"`
	Skills       []Source `json:"skill_catalogs"`
}

type sourceGroup struct {
	key     string
	sources []Source
}

func (c Config) groups() []sourceGroup {
	models := append([]Source(nil), c.Models...)
	ids := map[string]bool{}
	for _, s := range models {
		ids[s.ID] = true
	}
	for _, s := range c.LegacyModels {
		if !ids[s.ID] {
			models = append(models, s)
		}
	}
	return []sourceGroup{{"models", models}, {"mcp_servers", c.MCP}, {"agents", c.Agents}, {"serving_runtimes", c.Runtimes}, {"skills", c.Skills}}
}

const (
	maxYAMLBytes         = 32 << 20
	maxNDJSONRecordBytes = 16 << 20
)

func readBoundedFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxBytes)
	}
	return data, nil
}

func readYAML(path string, target any) error {
	data, err := readBoundedFile(path, maxYAMLBytes)
	if err != nil {
		return err
	}
	reader := yamlutil.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	if _, err := reader.Read(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for {
		next, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(next)) != 0 {
			return fmt.Errorf("%s: multiple YAML documents are not supported", path)
		}
	}
	data, err = yaml.YAMLToJSONStrict(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err = json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Sources returns the enabled, required shipped sources. Administrator source
// ConfigMaps are deliberately not passed to this validator.
func Sources(path string) ([]Source, error) {
	var config Config
	if err := readYAML(path, &config); err != nil {
		return nil, err
	}
	var sources []Source
	ids := map[string]bool{}
	for _, group := range config.groups() {
		for _, s := range group.sources {
			if strings.TrimSpace(s.ID) == "" || ids[s.ID] {
				return nil, fmt.Errorf("empty or duplicate source ID %q", s.ID)
			}
			ids[s.ID] = true
			if s.Enabled != nil && !*s.Enabled {
				continue
			}
			if group.key == "skills" {
				return nil, fmt.Errorf("shipped skill sources are not supported")
			}
			if s.Type != "yaml" || s.Properties.Path == "" {
				return nil, fmt.Errorf("source %s must specify a YAML catalog path", s.ID)
			}
			sources = append(sources, s)
		}
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("no enabled shipped sources in %s", path)
	}
	return sources, nil
}

func Validate(configPath, benchmarkPath string) *Failure {
	if err := validateCatalogs(configPath); err != nil {
		return &Failure{"Loading", "CatalogContentInvalid", err.Error()}
	}
	if err := validateBenchmarks(benchmarkPath); err != nil {
		return &Failure{"Loading", "BenchmarkContentInvalid", err.Error()}
	}
	return nil
}

func validateCatalogs(path string) error {
	if _, err := Sources(path); err != nil {
		return err
	}
	var config Config
	if err := readYAML(path, &config); err != nil {
		return err
	}
	for _, group := range config.groups() {
		for _, source := range group.sources {
			if source.Enabled != nil && !*source.Enabled {
				continue
			}
			dataPath := source.Properties.Path
			if !filepath.IsAbs(dataPath) {
				dataPath = filepath.Join(filepath.Dir(path), dataPath)
			}
			var document map[string]json.RawMessage
			if err := readYAML(dataPath, &document); err != nil {
				return fmt.Errorf("source %s: %w", source.ID, err)
			}
			raw, ok := document[group.key]
			if !ok || string(raw) == "null" {
				return fmt.Errorf("source %s: missing %s list", source.ID, group.key)
			}
			var entries []map[string]json.RawMessage
			if err := json.Unmarshal(raw, &entries); err != nil {
				return fmt.Errorf("source %s: %w", source.ID, err)
			}
			names := map[string]bool{}
			for index, entry := range entries {
				if err := validateEntry(group.key, entry, names); err != nil {
					return fmt.Errorf("source %s, %s[%d]: %w", source.ID, group.key, index, err)
				}
			}
		}
	}
	return nil
}

func stringField(entry map[string]json.RawMessage, key string) string {
	var s string
	_ = json.Unmarshal(entry[key], &s)
	return s
}
func validateEntry(kind string, entry map[string]json.RawMessage, seen map[string]bool) error {
	name := strings.TrimSpace(stringField(entry, "name"))
	if name == "" {
		return fmt.Errorf("name is required")
	}
	identity := name
	if kind == "mcp_servers" {
		identity += "\x00" + stringField(entry, "version")
	}
	if seen[identity] {
		return fmt.Errorf("duplicate name/version %q", name)
	}
	seen[identity] = true
	if raw, ok := entry["artifacts"]; ok {
		var artifacts []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &artifacts); err != nil {
			return err
		}
		for _, artifact := range artifacts {
			if artifact == nil {
				return fmt.Errorf("null artifact")
			}
			if kind == "models" {
				var artifactType string
				if raw, ok := artifact["artifactType"]; ok {
					if err := json.Unmarshal(raw, &artifactType); err != nil {
						return fmt.Errorf("artifactType must be a string: %w", err)
					}
				}
				switch artifactType {
				case "", "model-artifact":
					// Match the runtime's legacy default for omitted, null or empty types.
				case "metrics-artifact":
					continue
				default:
					return fmt.Errorf("unknown artifactType %q", artifactType)
				}
			}
			uri := stringField(artifact, "uri")
			parsed, err := url.Parse(uri)
			if err != nil || parsed == nil || !slices.Contains([]string{"oci", "http", "https", "s3", "gs", "az", "file"}, parsed.Scheme) {
				return fmt.Errorf("invalid artifact URI")
			}
		}
	}
	if kind == "mcp_servers" {
		if raw, ok := entry["tools"]; ok {
			var tools []map[string]json.RawMessage
			if err := json.Unmarshal(raw, &tools); err != nil {
				return err
			}
			for _, tool := range tools {
				if strings.TrimSpace(stringField(tool, "name")) == "" {
					return fmt.Errorf("tool name is required")
				}
			}
		}
		if raw, ok := entry["endpoints"]; ok {
			var endpoints map[string]string
			if err := json.Unmarshal(raw, &endpoints); err != nil {
				return err
			}
			for kind, endpoint := range endpoints {
				parsed, err := url.Parse(endpoint)
				if err != nil || parsed == nil || parsed.Host == "" || !slices.Contains([]string{"http", "https", "ws", "wss"}, parsed.Scheme) {
					return fmt.Errorf("invalid %s endpoint", kind)
				}
			}
		}
	}
	if kind == "serving_runtimes" {
		var versions []map[string]json.RawMessage
		if err := json.Unmarshal(entry["versions"], &versions); err != nil {
			return fmt.Errorf("versions: %w", err)
		}
		if len(versions) == 0 {
			return fmt.Errorf("versions are required")
		}
		seenVersions := map[string]bool{}
		for _, version := range versions {
			id := stringField(version, "version")
			if id == "" || seenVersions[id] || stringField(version, "image") == "" {
				return fmt.Errorf("each version requires a unique version and image")
			}
			seenVersions[id] = true
			for _, key := range []string{"servingRuntimeTemplate", "llmInferenceServiceConfig"} {
				if text := stringField(version, key); text != "" {
					var object map[string]any
					if err := yaml.UnmarshalStrict([]byte(text), &object); err != nil {
						return fmt.Errorf("%s: %w", key, err)
					}
					if object["kind"] == nil || object["apiVersion"] == nil {
						return fmt.Errorf("%s needs kind and apiVersion", key)
					}
				}
			}
		}
	}
	return nil
}

func validateBenchmarks(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("benchmark path is not a directory")
	}
	ids := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		switch entry.Name() {
		case "metadata.json":
			data, err := readBoundedFile(path, maxYAMLBytes)
			if err != nil {
				return err
			}
			var metadata struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(data, &metadata); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			id := strings.ToLower(strings.TrimSpace(metadata.ID))
			if id == "" || ids[id] {
				return fmt.Errorf("%s: empty or duplicate model id", path)
			}
			ids[id] = true
		case "evaluations.ndjson", "performance.ndjson", "security-evaluations.ndjson":
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "metadata.json")); err != nil {
				return fmt.Errorf("%s requires metadata.json", path)
			}
			return validateNDJSON(path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}
func validateNDJSON(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxNDJSONRecordBytes)
	for line := 1; scanner.Scan(); line++ {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var record map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return fmt.Errorf("%s:%d: %w", path, line, err)
		}
		// The runtime associates records with the adjacent metadata.json and
		// accepts dynamic metric fields. Published evaluations can have a
		// numeric id or dataset/metric without model_id or benchmark; published
		// performance records use config_id without model_id.
		if record == nil {
			return fmt.Errorf("%s:%d: record must be a JSON object", path, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
