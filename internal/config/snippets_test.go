package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// TestDocsConfigSnippets verifies that every snippet under docs/snippets/
// has all three variants (.toml, .yaml, .json), and that all three parse
// cleanly and decode to identical configuration values.
func TestDocsConfigSnippets(t *testing.T) {
	snippetsDir := filepath.Join("..", "..", "docs", "snippets")
	entries, err := os.ReadDir(snippetsDir)
	if err != nil {
		t.Fatalf("read docs/snippets: %v", err)
	}

	bases := make(map[string]bool)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext == ".toml" || ext == ".yaml" || ext == ".yml" || ext == ".json" {
			base := strings.TrimSuffix(e.Name(), ext)
			bases[base] = true
		}
	}

	if len(bases) == 0 {
		t.Fatal("no config snippets found in docs/snippets")
	}

	for base := range bases {
		t.Run(base, func(t *testing.T) {
			tomlPath := filepath.Join(snippetsDir, base+".toml")
			yamlPath := filepath.Join(snippetsDir, base+".yaml")
			jsonPath := filepath.Join(snippetsDir, base+".json")

			tomlBytes, err := os.ReadFile(tomlPath)
			if err != nil {
				t.Fatalf("missing TOML variant: %v", err)
			}
			yamlBytes, err := os.ReadFile(yamlPath)
			if err != nil {
				t.Fatalf("missing YAML variant: %v", err)
			}
			jsonBytes, err := os.ReadFile(jsonPath)
			if err != nil {
				t.Fatalf("missing JSON variant: %v", err)
			}

			// First, try decoding as a full RepoConfig.
			tomlCfg, tomlErr := ParseRepoConfig(string(tomlBytes), base+".toml")
			yamlCfg, yamlErr := ParseRepoConfig(string(yamlBytes), base+".yaml")
			jsonCfg, jsonErr := ParseRepoConfig(string(jsonBytes), base+".json")

			if tomlErr == nil {
				if yamlErr != nil {
					t.Fatalf("YAML ParseRepoConfig failed: %v", yamlErr)
				}
				if jsonErr != nil {
					t.Fatalf("JSON ParseRepoConfig failed: %v", jsonErr)
				}
				if !reflect.DeepEqual(tomlCfg, yamlCfg) {
					t.Errorf("TOML and YAML configs differ:\nTOML: %+v\nYAML: %+v", tomlCfg, yamlCfg)
				}
				if !reflect.DeepEqual(tomlCfg, jsonCfg) {
					t.Errorf("TOML and JSON configs differ:\nTOML: %+v\nJSON: %+v", tomlCfg, jsonCfg)
				}
				return
			}

			// If not a full RepoConfig, decode as a runner or queue fragment.
			type runnerFragment struct {
				Runners map[string]Runner `toml:"runners" yaml:"runners" json:"runners"`
			}
			type queueFragment struct {
				Queues map[string]Queue `toml:"queues" yaml:"queues" json:"queues"`
			}

			var tomlRunners, yamlRunners, jsonRunners runnerFragment
			_, errTomlR := toml.Decode(string(tomlBytes), &tomlRunners)
			errYamlR := yaml.Unmarshal(yamlBytes, &yamlRunners)
			errJsonR := json.Unmarshal(jsonBytes, &jsonRunners)

			if errTomlR == nil && errYamlR == nil && errJsonR == nil && len(tomlRunners.Runners) > 0 {
				if !reflect.DeepEqual(tomlRunners, yamlRunners) {
					t.Errorf("TOML and YAML runner fragments differ:\nTOML: %+v\nYAML: %+v", tomlRunners, yamlRunners)
				}
				if !reflect.DeepEqual(tomlRunners, jsonRunners) {
					t.Errorf("TOML and JSON runner fragments differ:\nTOML: %+v\nJSON: %+v", tomlRunners, jsonRunners)
				}
				return
			}

			var tomlQueues, yamlQueues, jsonQueues queueFragment
			_, errTomlQ := toml.Decode(string(tomlBytes), &tomlQueues)
			errYamlQ := yaml.Unmarshal(yamlBytes, &yamlQueues)
			errJsonQ := json.Unmarshal(jsonBytes, &jsonQueues)

			if errTomlQ == nil && errYamlQ == nil && errJsonQ == nil && len(tomlQueues.Queues) > 0 {
				if !reflect.DeepEqual(tomlQueues, yamlQueues) {
					t.Errorf("TOML and YAML queue fragments differ:\nTOML: %+v\nYAML: %+v", tomlQueues, yamlQueues)
				}
				if !reflect.DeepEqual(tomlQueues, jsonQueues) {
					t.Errorf("TOML and JSON queue fragments differ:\nTOML: %+v\nJSON: %+v", tomlQueues, jsonQueues)
				}
				return
			}

			// Neutral fallback comparison
			var rawToml, rawYaml, rawJson any
			if _, err := toml.Decode(string(tomlBytes), &rawToml); err != nil {
				t.Fatalf("TOML decode failed: %v", err)
			}
			if err := yaml.Unmarshal(yamlBytes, &rawYaml); err != nil {
				t.Fatalf("YAML decode failed: %v", err)
			}
			if err := json.Unmarshal(jsonBytes, &rawJson); err != nil {
				t.Fatalf("JSON decode failed: %v", err)
			}

			normToml := normalizeNeutral(rawToml)
			normYaml := normalizeNeutral(rawYaml)
			normJson := normalizeNeutral(rawJson)

			if !reflect.DeepEqual(normToml, normYaml) {
				t.Errorf("TOML and YAML generic structures differ:\nTOML: %+v\nYAML: %+v", normToml, normYaml)
			}
			if !reflect.DeepEqual(normToml, normJson) {
				t.Errorf("TOML and JSON generic structures differ:\nTOML: %+v\nJSON: %+v", normToml, normJson)
			}
		})
	}
}

func normalizeNeutral(v any) any {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case map[string]any:
		res := make(map[string]any, len(val))
		for k, item := range val {
			res[k] = normalizeNeutral(item)
		}
		return res
	case map[any]any:
		res := make(map[string]any, len(val))
		for k, item := range val {
			res[fmt.Sprint(k)] = normalizeNeutral(item)
		}
		return res
	case []any:
		res := make([]any, len(val))
		for i, item := range val {
			res[i] = normalizeNeutral(item)
		}
		return res
	case int:
		return int64(val)
	case int32:
		return int64(val)
	case float64:
		if float64(int64(val)) == val {
			return int64(val)
		}
		return val
	default:
		return val
	}
}
