package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// RepoConfigNames lists the candidate repo config filenames in the order
// errors report them.
var RepoConfigNames = []string{
	"lerp.toml",
	"lerp.yaml",
	"lerp.yml",
	"lerp.json",
}

// ParseRepoConfig decodes and validates repo config source; label names the
// origin (a file path) in errors. Format is determined by the label's
// extension (.toml, .yaml, .yml, .json), defaulting to TOML when the
// extension is unrecognised.
func ParseRepoConfig(source, label string) (*RepoConfig, error) {
	ext := strings.ToLower(filepath.Ext(label))
	var c RepoConfig
	var raw map[string]any

	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal([]byte(source), &c); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		if err := yaml.Unmarshal([]byte(source), &raw); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
	case ".json":
		if err := json.Unmarshal([]byte(source), &c); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		if err := json.Unmarshal([]byte(source), &raw); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
	default: // .toml or unrecognised extension (e.g. "stock")
		if _, err := toml.Decode(source, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		if _, err := toml.Decode(source, &raw); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
	}

	if keys := unknownKeys(raw, reflect.TypeOf(RepoConfig{}), ""); len(keys) > 0 {
		return nil, fmt.Errorf("%s: unknown key(s): %s", label, strings.Join(keys, ", "))
	}
	if err := c.validate(label); err != nil {
		return nil, err
	}
	c.resolveVendors()
	return &c, nil
}

// unknownKeys performs a reflection walk over raw (the neutral map[string]any
// from decoding) comparing keys against struct tags of type t. It recurses into
// map-valued fields (Runners, Queues) by element type, stops at the shallowest
// unknown key, and returns a sorted slice of dotted paths.
func unknownKeys(raw map[string]any, t reflect.Type, prefix string) []string {
	if raw == nil {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}

	validFields := make(map[string]reflect.StructField, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("toml")
		if tag == "" {
			tag = f.Tag.Get("yaml")
		}
		if tag == "" {
			tag = f.Tag.Get("json")
		}
		if tag == "" || tag == "-" {
			continue
		}
		tagName := strings.Split(tag, ",")[0]
		validFields[tagName] = f
	}

	var unknown []string
	for k, v := range raw {
		f, ok := validFields[k]
		if !ok {
			fullKey := k
			if prefix != "" {
				fullKey = prefix + "." + k
			}
			unknown = append(unknown, fullKey)
			continue
		}

		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}

		if ft.Kind() == reflect.Map {
			elemType := ft.Elem()
			if vMap, ok := toMap(v); ok {
				for mk, mv := range vMap {
					elemPrefix := k + "." + mk
					if prefix != "" {
						elemPrefix = prefix + "." + k + "." + mk
					}
					if mvMap, ok := toMap(mv); ok {
						unknown = append(unknown, unknownKeys(mvMap, elemType, elemPrefix)...)
					}
				}
			}
		} else if ft.Kind() == reflect.Struct {
			if vMap, ok := toMap(v); ok {
				structPrefix := k
				if prefix != "" {
					structPrefix = prefix + "." + k
				}
				unknown = append(unknown, unknownKeys(vMap, ft, structPrefix)...)
			}
		}
	}

	slices.Sort(unknown)
	return unknown
}

func toMap(val any) (map[string]any, bool) {
	if val == nil {
		return nil, false
	}
	if m, ok := val.(map[string]any); ok {
		return m, true
	}
	if m, ok := val.(map[any]any); ok {
		res := make(map[string]any, len(m))
		for k, v := range m {
			res[fmt.Sprint(k)] = v
		}
		return res, true
	}
	return nil, false
}
