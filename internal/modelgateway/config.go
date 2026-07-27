package modelgateway

import "strings"

// PoolConfigFromLookup converts the documented CTF_MODEL* environment keys into
// a PoolConfig. The lookup is injected so a fresh .env can be tested without
// changing the daemon process environment or its active gateway.
func PoolConfigFromLookup(lookup func(string) string) PoolConfig {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	names := strings.Split(lookup("CTF_MODELS"), ",")
	config := PoolConfig{DefaultModel: strings.TrimSpace(lookup("CTF_DEFAULT_MODEL"))}
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		prefix := "CTF_MODEL_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_"
		config.Models = append(config.Models, ModelConfig{
			Name: name, UpstreamBaseURL: lookup(prefix + "BASE_URL"), UpstreamAPIKey: lookup(prefix + "API_KEY"),
			ModelID: lookup(prefix + "ID"), IncludeStreamUsage: streamUsageEnabled(lookup(prefix + "INCLUDE_STREAM_USAGE")),
			SupportsImages: imageInputEnabled(lookup(prefix + "SUPPORTS_IMAGES")),
		})
	}
	if len(config.Models) == 0 {
		legacy := ModelConfig{
			Name: "default", UpstreamBaseURL: lookup("CTF_UPSTREAM_MODEL_BASE_URL"),
			UpstreamAPIKey: lookup("CTF_UPSTREAM_MODEL_API_KEY"), ModelID: lookup("CTF_MODEL_ID"),
			IncludeStreamUsage: streamUsageEnabled(lookup("CTF_MODEL_INCLUDE_STREAM_USAGE")),
			SupportsImages:     imageInputEnabled(lookup("CTF_MODEL_SUPPORTS_IMAGES")),
		}
		// An entirely empty .env represents an empty model list. Preserve a
		// partially configured legacy profile so the editor can still repair it.
		if strings.TrimSpace(legacy.UpstreamBaseURL) != "" || strings.TrimSpace(legacy.UpstreamAPIKey) != "" || strings.TrimSpace(legacy.ModelID) != "" {
			config.Models = append(config.Models, legacy)
			if config.DefaultModel == "" {
				config.DefaultModel = "default"
			}
		} else {
			config.DefaultModel = ""
		}
	}
	return config
}

func streamUsageEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// imageInputEnabled remains opt-in so text-only OpenAI-compatible providers
// such as DeepSeek never receive unsupported image_url content blocks.
func imageInputEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
