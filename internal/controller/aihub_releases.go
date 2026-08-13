/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
)

type componentMetadata struct {
	Releases []common.ComponentRelease `json:"releases"`
}

var fallbackReleases = []common.ComponentRelease{
	{Name: "model-registry-operator", Version: "unknown", RepoURL: "https://github.com/opendatahub-io/model-registry-operator"},
}

func loadComponentReleases(manifestsPath string, componentDirs []string) ([]common.ComponentRelease, error) {
	seen := make(map[string]bool)
	var all []common.ComponentRelease

	for _, dir := range componentDirs {
		path := filepath.Join(manifestsPath, dir, "component_metadata.yaml")

		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}

		var meta componentMetadata
		if err := yaml.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}

		for _, r := range meta.Releases {
			if r.Name == "" || seen[r.Name] {
				continue
			}
			if r.Version == "" {
				r.Version = "unknown"
			}
			seen[r.Name] = true
			all = append(all, r)
		}
	}

	if len(all) == 0 {
		return fallbackReleases, nil
	}

	return all, nil
}
