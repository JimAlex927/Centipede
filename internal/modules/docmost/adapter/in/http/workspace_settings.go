package http

import "encoding/json"

func setWorkspaceSetting(settings json.RawMessage, value *bool, path ...string) json.RawMessage {
	if value == nil {
		return settings
	}
	return setWorkspaceValue(settings, *value, path...)
}

func setWorkspaceScalarSetting(settings json.RawMessage, key, value string) json.RawMessage {
	return setWorkspaceValue(settings, value, key)
}

func setWorkspaceValue(settings json.RawMessage, value any, path ...string) json.RawMessage {
	values := map[string]any{}
	if len(settings) > 0 {
		_ = json.Unmarshal(settings, &values)
	}
	if len(path) == 0 {
		return settings
	}
	current := values
	for _, key := range path[:len(path)-1] {
		nested, _ := current[key].(map[string]any)
		if nested == nil {
			nested = map[string]any{}
			current[key] = nested
		}
		current = nested
	}
	current[path[len(path)-1]] = value
	encoded, err := json.Marshal(values)
	if err != nil {
		return settings
	}
	return encoded
}
