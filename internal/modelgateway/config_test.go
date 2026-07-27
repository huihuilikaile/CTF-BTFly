package modelgateway

import "testing"

func TestPoolConfigFromLookupKeepsEmptyModelListEmpty(t *testing.T) {
	config := PoolConfigFromLookup(func(string) string { return "" })
	if len(config.Models) != 0 || config.DefaultModel != "" {
		t.Fatalf("empty lookup produced %#v", config)
	}
}

func TestPoolConfigFromLookupPreservesPartialLegacyProfile(t *testing.T) {
	config := PoolConfigFromLookup(func(key string) string {
		if key == "CTF_UPSTREAM_MODEL_BASE_URL" {
			return "https://example.test/v1"
		}
		return ""
	})
	if len(config.Models) != 1 || config.Models[0].Name != "default" || config.DefaultModel != "default" {
		t.Fatalf("partial legacy lookup produced %#v", config)
	}
}
