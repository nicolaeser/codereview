package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type keysValue struct {
	Key            string
	BaseURL        string
	AuthMode       string
	Models         []string
	ZDR            *bool
	DataCollection *string
	Object         bool
}

type customProvider struct {
	BaseURL  string
	AuthMode string
}

type keysPrivacyOverlay struct {
	zdr            *bool
	dataCollection *string
}

func loadProviderKeys(enabled []string) (map[string]string, map[string]customProvider, map[string]keysPrivacyOverlay, map[string][]string, error) {
	if raw := strings.TrimSpace(os.Getenv("AI_KEYS_JSON")); raw != "" {
		values, err := parseKeysDocument(raw, "AI_KEYS_JSON")
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if err := constrainKeysJSON(values, enabled); err != nil {
			return nil, nil, nil, nil, err
		}
		return materializeKeys(values)
	}
	path := strings.TrimSpace(os.Getenv("AI_KEYS_FILE"))
	if path == "" {
		return map[string]string{}, map[string]customProvider{}, map[string]keysPrivacyOverlay{}, map[string][]string{}, nil
	}
	values, err := syncKeysFile(path, enabled)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("AI_KEYS_FILE: %w", err)
	}
	return materializeKeys(values)
}

func parseKeysDocument(raw, source string) (map[string]keysValue, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]keysValue{}, nil
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	out := make(map[string]keysValue, len(parsed))
	for name, item := range parsed {
		id, err := requestedAllowlistID(name)
		if err != nil {
			return nil, fmt.Errorf("%s contains unsupported provider %q", source, name)
		}
		value, err := parseKeysValue(item, id, source)
		if err != nil {
			return nil, err
		}
		if existing, ok := out[id]; ok && !keysValueEqual(existing, value) {
			return nil, fmt.Errorf("%s maps multiple keys to provider %q", source, id)
		}
		out[id] = value
	}
	return out, nil
}

func parseKeysValue(raw json.RawMessage, id, source string) (keysValue, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return keysValue{}, nil
	}
	if trimmed[0] == '"' {
		var key string
		if err := json.Unmarshal(trimmed, &key); err != nil {
			return keysValue{}, fmt.Errorf("%s provider %q: %w", source, id, err)
		}
		if isCustomProviderID(id) {
			return keysValue{}, fmt.Errorf("%s custom provider %q must be an object with base_url", source, id)
		}
		return keysValue{Key: strings.TrimSpace(key)}, nil
	}
	if trimmed[0] != '{' {
		return keysValue{}, fmt.Errorf("%s provider %q must be a string or object", source, id)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return keysValue{}, fmt.Errorf("%s provider %q: %w", source, id, err)
	}
	value := keysValue{Object: true}
	for field, fieldRaw := range parsed {
		switch strings.ToLower(strings.TrimSpace(field)) {
		case "key", "api_key":
			if err := json.Unmarshal(fieldRaw, &value.Key); err != nil {
				return keysValue{}, fmt.Errorf("%s provider %q key: %w", source, id, err)
			}
			value.Key = strings.TrimSpace(value.Key)
		case "base_url":
			if err := json.Unmarshal(fieldRaw, &value.BaseURL); err != nil {
				return keysValue{}, fmt.Errorf("%s provider %q base_url: %w", source, id, err)
			}
			value.BaseURL = strings.TrimRight(strings.TrimSpace(value.BaseURL), "/")
		case "auth_mode":
			if err := json.Unmarshal(fieldRaw, &value.AuthMode); err != nil {
				return keysValue{}, fmt.Errorf("%s provider %q auth_mode: %w", source, id, err)
			}
			value.AuthMode = strings.ToLower(strings.TrimSpace(value.AuthMode))
		case "model":
			var model string
			if err := json.Unmarshal(fieldRaw, &model); err != nil {
				return keysValue{}, fmt.Errorf("%s provider %q model: %w", source, id, err)
			}
			model = strings.TrimSpace(model)
			if model == "" {
				return keysValue{}, fmt.Errorf("%s provider %q model must not be empty", source, id)
			}
			if len(value.Models) > 0 {
				return keysValue{}, fmt.Errorf("%s provider %q must not set both model and models", source, id)
			}
			value.Models = []string{model}
		case "models":
			var models []string
			if err := json.Unmarshal(fieldRaw, &models); err != nil {
				return keysValue{}, fmt.Errorf("%s provider %q models: %w", source, id, err)
			}
			if len(models) == 0 {
				return keysValue{}, fmt.Errorf("%s provider %q models must not be empty", source, id)
			}
			if len(value.Models) > 0 {
				return keysValue{}, fmt.Errorf("%s provider %q must not set both model and models", source, id)
			}
			for i, model := range models {
				model = strings.TrimSpace(model)
				if model == "" {
					return keysValue{}, fmt.Errorf("%s provider %q models[%d] must not be empty", source, id, i)
				}
				models[i] = model
			}
			value.Models = models
		case "protocol":
			var protocol string
			if err := json.Unmarshal(fieldRaw, &protocol); err != nil {
				return keysValue{}, fmt.Errorf("%s provider %q protocol: %w", source, id, err)
			}
			protocol = strings.ToLower(strings.TrimSpace(protocol))
			if protocol != "" && protocol != "openai" {
				return keysValue{}, fmt.Errorf("%s provider %q protocol must be openai", source, id)
			}
		case "zdr":
			flag, err := parseOptionalBool(fieldRaw)
			if err != nil {
				return keysValue{}, fmt.Errorf("%s provider %q zdr: %w", source, id, err)
			}
			value.ZDR = flag
		case "data_collection", "data_retention":
			var mode string
			if err := json.Unmarshal(fieldRaw, &mode); err != nil {
				return keysValue{}, fmt.Errorf("%s provider %q data_collection: %w", source, id, err)
			}
			mode = strings.ToLower(strings.TrimSpace(mode))
			if mode != "allow" && mode != "deny" {
				return keysValue{}, fmt.Errorf("%s provider %q data_collection must be allow or deny", source, id)
			}
			if value.DataCollection != nil && *value.DataCollection != mode {
				return keysValue{}, fmt.Errorf("%s provider %q has conflicting data_collection/data_retention", source, id)
			}
			value.DataCollection = &mode
		default:
			return keysValue{}, fmt.Errorf("%s provider %q has unsupported field %q", source, id, field)
		}
	}
	if isCustomProviderID(id) {
		if u := strings.TrimSpace(value.BaseURL); u != "" && !validHTTPURL(u) {
			return keysValue{}, fmt.Errorf("%s provider %q base_url must be an absolute http(s) URL", source, id)
		}
		if value.ZDR != nil || value.DataCollection != nil {
			return keysValue{}, fmt.Errorf("%s provider %q: zdr and data_collection are OpenRouter-only", source, id)
		}
		if value.AuthMode != "" && value.AuthMode != "bearer" && value.AuthMode != "x-api-key" && value.AuthMode != "api-key" && value.AuthMode != "none" {
			return keysValue{}, fmt.Errorf("%s provider %q has unsupported auth_mode", source, id)
		}
		return value, nil
	}
	if strings.TrimSpace(value.BaseURL) != "" {
		return keysValue{}, fmt.Errorf("%s provider %q cannot set base_url; use a custom provider name", source, id)
	}
	if id != "openrouter" && id != "openrouter-eu" && (value.ZDR != nil || value.DataCollection != nil) {
		return keysValue{}, fmt.Errorf("%s provider %q: zdr and data_collection are OpenRouter-only", source, id)
	}
	return value, nil
}

func parseOptionalBool(raw json.RawMessage) (*bool, error) {
	var flag bool
	if err := json.Unmarshal(raw, &flag); err == nil {
		return &flag, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, err
	}
	flag, err := strconv.ParseBool(strings.TrimSpace(text))
	if err != nil {
		return nil, err
	}
	return &flag, nil
}

func constrainKeysJSON(values map[string]keysValue, enabled []string) error {
	allowed := enabledSet(enabled)
	for id := range values {
		if _, ok := allowed[id]; !ok {
			return fmt.Errorf("AI_KEYS_JSON contains provider %q which is not in AI_PROVIDER", id)
		}
	}
	return nil
}

func syncKeysFile(path string, enabled []string) (map[string]keysValue, error) {
	info, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil && info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	values := map[string]keysValue{}
	if err == nil {
		raw, readErr := readBoundedFile(path, maxAIKeysFileBytes)
		if readErr != nil {
			return nil, readErr
		}
		if strings.TrimSpace(string(raw)) != "" {
			values, err = parseKeysDocument(string(raw), "AI_KEYS_FILE")
			if err != nil {
				return nil, err
			}
		}
	}
	next := make(map[string]keysValue, len(enabled))
	for _, id := range enabled {
		if id == "" {
			continue
		}
		if value, ok := values[id]; ok {
			next[id] = value
			continue
		}
		if id == "openrouter-eu" {
			if value, ok := values["openrouter"]; ok {
				if _, openrouterEnabled := enabledSet(enabled)["openrouter"]; !openrouterEnabled {
					next[id] = value
					continue
				}
			}
		}
		next[id] = keysValue{Object: true}
	}
	if err := writeKeysFileIfChanged(path, next); err != nil {
		return nil, err
	}
	return next, nil
}

func writeKeysFileIfChanged(path string, values map[string]keysValue) error {
	want, err := json.MarshalIndent(encodeKeys(values), "", "  ")
	if err != nil {
		return err
	}
	want = append(want, '\n')
	if int64(len(want)) > maxAIKeysFileBytes {
		return fmt.Errorf("file exceeds %d bytes", maxAIKeysFileBytes)
	}
	if raw, err := os.ReadFile(path); err == nil && bytes.Equal(raw, want) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ai-keys-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(want); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func encodeKeys(values map[string]keysValue) map[string]any {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make(map[string]any, len(ids))
	for _, id := range ids {
		value := values[id]
		if isCustomProviderID(id) || value.Object || value.BaseURL != "" || value.AuthMode != "" || len(value.Models) > 0 || value.ZDR != nil || value.DataCollection != nil {
			item := map[string]any{"key": value.Key}
			if isCustomProviderID(id) || value.BaseURL != "" {
				item["base_url"] = value.BaseURL
			}
			if value.AuthMode != "" {
				item["auth_mode"] = value.AuthMode
			}
			if len(value.Models) == 1 {
				item["model"] = value.Models[0]
			} else if len(value.Models) > 1 {
				item["models"] = value.Models
			}
			if value.ZDR != nil {
				item["zdr"] = *value.ZDR
			}
			if value.DataCollection != nil {
				item["data_collection"] = *value.DataCollection
			}
			out[id] = item
			continue
		}
		out[id] = value.Key
	}
	return out
}

func materializeKeys(values map[string]keysValue) (map[string]string, map[string]customProvider, map[string]keysPrivacyOverlay, map[string][]string, error) {
	keys := make(map[string]string, len(values))
	customs := map[string]customProvider{}
	privacy := map[string]keysPrivacyOverlay{}
	models := map[string][]string{}
	for id, value := range values {
		if strings.TrimSpace(value.Key) != "" {
			keys[id] = value.Key
		}
		if isCustomProviderID(id) {
			auth := value.AuthMode
			if auth == "" {
				auth = "bearer"
			}
			customs[id] = customProvider{BaseURL: value.BaseURL, AuthMode: auth}
		}
		if id == "openrouter" || id == "openrouter-eu" {
			privacy[id] = keysPrivacyOverlay{zdr: value.ZDR, dataCollection: value.DataCollection}
		}
		if len(value.Models) > 0 {
			models[id] = append([]string{}, value.Models...)
		}
	}
	return keys, customs, privacy, models, nil
}

func enabledProviderIDs(cfg *Config) []string {
	return append([]string{}, cfg.enabledProviders...)
}

func enabledSet(enabled []string) map[string]struct{} {
	out := make(map[string]struct{}, len(enabled))
	for _, id := range enabled {
		out[id] = struct{}{}
	}
	return out
}

func keysValueEqual(a, b keysValue) bool {
	if a.Key != b.Key || a.BaseURL != b.BaseURL || a.AuthMode != b.AuthMode {
		return false
	}
	if len(a.Models) != len(b.Models) {
		return false
	}
	for i := range a.Models {
		if a.Models[i] != b.Models[i] {
			return false
		}
	}
	if (a.ZDR == nil) != (b.ZDR == nil) || (a.ZDR != nil && *a.ZDR != *b.ZDR) {
		return false
	}
	if (a.DataCollection == nil) != (b.DataCollection == nil) || (a.DataCollection != nil && *a.DataCollection != *b.DataCollection) {
		return false
	}
	return true
}

func validCustomProviderName(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for i, r := range value {
		if i == 0 {
			if r < 'a' || r > 'z' {
				return false
			}
			continue
		}
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	if strings.Contains(value, "--") || strings.HasSuffix(value, "-") {
		return false
	}
	normalized, _ := normalizeProvider(value)
	return !validAIProvider(normalized)
}

func isCustomProviderID(id string) bool {
	return !validAIProvider(id) && id != "openrouter-eu" && validCustomProviderName(id)
}
