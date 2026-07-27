package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ctfagentpi/ctfagentpi/internal/envfile"
	"github.com/ctfagentpi/ctfagentpi/internal/modelgateway"
	"github.com/ctfagentpi/ctfagentpi/internal/platform"
	"github.com/go-chi/chi/v5"
)

// ModelConfigProbe reloads the freshly-read .env and tests the selected model.
// The daemon's live manager preserves pools that still serve task credentials.
type ModelConfigProbe func(context.Context, string) (modelgateway.ProbeStatus, error)

// SetModelConfigProbe installs the daemon-owned reload-and-probe callback.
func (s *Server) SetModelConfigProbe(probe ModelConfigProbe) { s.modelConfigProbe = probe }

// ModelProbeResult adds the source of a connection result without exposing an
// API key, request body, or raw upstream response.
type ModelProbeResult struct {
	modelgateway.ProbeStatus
	ConfigLoaded bool `json:"configLoaded"`
}

// ModelConfigSummary is the safe subset of a persisted model entry. API keys
// are represented only by a presence flag and are never sent back to the UI.
type ModelConfigSummary struct {
	Name               string `json:"name"`
	BaseURL            string `json:"baseUrl"`
	ModelID            string `json:"modelId"`
	Configured         bool   `json:"configured"`
	HasAPIKey          bool   `json:"hasApiKey"`
	SupportsImages     bool   `json:"supportsImages"`
	IncludeStreamUsage bool   `json:"includeStreamUsage"`
	Default            bool   `json:"default"`
}

// ModelConfigList is the persisted .env view shown by the model manager.
type ModelConfigList struct {
	Models       []ModelConfigSummary `json:"models"`
	DefaultModel string               `json:"defaultModel"`
}

// ModelConfigInput is accepted only from the authenticated local desktop UI.
// APIKey is write-only and is intentionally absent from all response types.
type ModelConfigInput struct {
	Name               string `json:"name"`
	BaseURL            string `json:"baseUrl"`
	APIKey             string `json:"apiKey"`
	ModelID            string `json:"modelId"`
	SupportsImages     bool   `json:"supportsImages"`
	IncludeStreamUsage bool   `json:"includeStreamUsage"`
	Default            bool   `json:"default"`
}

func (s *Server) modelConfigs(writer http.ResponseWriter, request *http.Request) {
	result, err := readModelConfigList()
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) saveModelConfig(writer http.ResponseWriter, request *http.Request) {
	var input ModelConfigInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := validateModelConfig(&input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}

	path, err := envfile.ConfigFile()
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	values, err := envfile.Read(path)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	updates, err := modelConfigUpdates(values, input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := envfile.Update(path, updates); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	result, err := readModelConfigList()
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) deleteModelConfig(writer http.ResponseWriter, request *http.Request) {
	name, err := normalizeModelName(chi.URLParam(request, "profile"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if s.store == nil {
		writeError(writer, http.StatusServiceUnavailable, fmt.Errorf("task store is unavailable"))
		return
	}
	tasks, err := s.store.ListTasks(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	profile, _ := s.gateway.Profile(name)
	defaultProfile := s.gateway.DefaultProfile()
	blocking := make([]map[string]string, 0)
	for _, task := range tasks {
		usesModel := task.ModelProfile == name
		if strings.TrimSpace(task.ModelProfile) == "" {
			// Tasks created before model profiles were persisted only retain
			// model_id. Keep those historical, unfinished tasks protected too.
			usesModel = (profile.ModelID != "" && task.ModelID == profile.ModelID) ||
				(task.ModelID == "" && defaultProfile == name)
		}
		if usesModel && !modelDeletionTerminalStatus(task.Status) {
			blocking = append(blocking, map[string]string{"id": task.ID, "title": task.Title, "status": string(task.Status)})
		}
	}
	if len(blocking) > 0 {
		writeJSON(writer, http.StatusConflict, map[string]any{
			"error": fmt.Sprintf("模型 %s 仍被 %d 个未结束题目使用，请先结束这些题目后再删除", name, len(blocking)),
			"tasks": blocking,
		})
		return
	}

	path, err := envfile.ConfigFile()
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	values, err := envfile.Read(path)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	updates, nextDefault, err := modelConfigDeletionUpdates(values, name)
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	if err := envfile.Update(path, updates); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if s.modelConfigProbe != nil {
		if _, err := s.modelConfigProbe(request.Context(), nextDefault); err != nil {
			writeError(writer, http.StatusInternalServerError, fmt.Errorf("reload model configuration: %w", err))
			return
		}
	}
	result, err := readModelConfigList()
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func modelDeletionTerminalStatus(status platform.TaskStatus) bool {
	return status == platform.TaskSettled || status == platform.TaskFailed || status == platform.TaskCancelled
}

func readModelConfigList() (ModelConfigList, error) {
	path, err := envfile.ConfigFile()
	if err != nil {
		return ModelConfigList{}, err
	}
	values, err := envfile.Read(path)
	if err != nil {
		return ModelConfigList{}, err
	}
	config := modelgateway.PoolConfigFromLookup(func(key string) string { return values[key] })
	result := ModelConfigList{DefaultModel: config.DefaultModel, Models: make([]ModelConfigSummary, 0, len(config.Models))}
	for _, model := range config.Models {
		result.Models = append(result.Models, summarizeModelConfig(model, config.DefaultModel))
	}
	return result, nil
}

func summarizeModelConfig(model modelgateway.ModelConfig, defaultModel string) ModelConfigSummary {
	baseURL := redactedBaseURL(model.UpstreamBaseURL)
	hasKey := strings.TrimSpace(model.UpstreamAPIKey) != ""
	return ModelConfigSummary{
		Name: model.Name, BaseURL: baseURL, ModelID: model.ModelID,
		Configured: baseURL != "" && hasKey && strings.TrimSpace(model.ModelID) != "",
		HasAPIKey:  hasKey, SupportsImages: model.SupportsImages,
		IncludeStreamUsage: model.IncludeStreamUsage, Default: model.Name == defaultModel,
	}
}

func validateModelConfig(input *ModelConfigInput) error {
	name, err := normalizeModelName(input.Name)
	if err != nil {
		return err
	}
	input.Name = name
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	endpoint, err := url.ParseRequestURI(input.BaseURL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return fmt.Errorf("model base URL must be a valid http(s) URL")
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return fmt.Errorf("model base URL must not include credentials, query parameters, or fragments")
	}
	input.ModelID = strings.TrimSpace(input.ModelID)
	if input.ModelID == "" || len(input.ModelID) > 256 || strings.ContainsAny(input.ModelID, "\r\n") {
		return fmt.Errorf("model ID is required and must be a single line of at most 256 characters")
	}
	if strings.ContainsAny(input.APIKey, "\r\n") {
		return fmt.Errorf("API key must be a single line")
	}
	return nil
}

func normalizeModelName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if len(name) == 0 || len(name) > 32 || name[0] < 'a' || name[0] > 'z' {
		return "", fmt.Errorf("model profile name must start with a lowercase letter and be at most 32 characters")
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
			continue
		}
		return "", fmt.Errorf("model profile name may contain only lowercase letters, digits, and hyphens")
	}
	return name, nil
}

func modelConfigUpdates(values map[string]string, input ModelConfigInput) (map[string]*string, error) {
	config := modelgateway.PoolConfigFromLookup(func(key string) string { return values[key] })
	multiModel := strings.TrimSpace(values["CTF_MODELS"]) != ""
	names := make([]string, 0, len(config.Models))
	existing := false
	existingHasKey := false
	for _, model := range config.Models {
		if multiModel || configuredModel(model) {
			names = append(names, model.Name)
		}
		if model.Name == input.Name {
			existing = true
			existingHasKey = strings.TrimSpace(model.UpstreamAPIKey) != ""
		}
	}
	if strings.TrimSpace(input.APIKey) == "" && (!existing || !existingHasKey) {
		return nil, fmt.Errorf("API key is required for a new or unkeyed model profile")
	}

	updates := make(map[string]*string)
	if !multiModel && input.Name == "default" {
		setModelValue(updates, "CTF_UPSTREAM_MODEL_BASE_URL", input.BaseURL)
		setModelValue(updates, "CTF_MODEL_ID", input.ModelID)
		setModelValue(updates, "CTF_MODEL_INCLUDE_STREAM_USAGE", boolString(input.IncludeStreamUsage))
		setModelValue(updates, "CTF_MODEL_SUPPORTS_IMAGES", boolString(input.SupportsImages))
		if strings.TrimSpace(input.APIKey) != "" {
			setModelValue(updates, "CTF_UPSTREAM_MODEL_API_KEY", input.APIKey)
		}
		if input.Default {
			setModelValue(updates, "CTF_DEFAULT_MODEL", "default")
		}
		return updates, nil
	}

	if !multiModel {
		if len(config.Models) > 0 && configuredModel(config.Models[0]) {
			legacy := config.Models[0]
			names = []string{"default"}
			setProfileValues(updates, "default", legacy)
		} else {
			names = nil
		}
	}
	if !containsModelName(names, input.Name) {
		names = append(names, input.Name)
	}
	setModelValue(updates, "CTF_MODELS", strings.Join(names, ","))
	setModelValue(updates, profileKey(input.Name, "BASE_URL"), input.BaseURL)
	setModelValue(updates, profileKey(input.Name, "ID"), input.ModelID)
	setModelValue(updates, profileKey(input.Name, "INCLUDE_STREAM_USAGE"), boolString(input.IncludeStreamUsage))
	setModelValue(updates, profileKey(input.Name, "SUPPORTS_IMAGES"), boolString(input.SupportsImages))
	if strings.TrimSpace(input.APIKey) != "" {
		setModelValue(updates, profileKey(input.Name, "API_KEY"), input.APIKey)
	}
	defaultModel := config.DefaultModel
	if input.Default || !containsModelName(names, defaultModel) {
		defaultModel = input.Name
	}
	setModelValue(updates, "CTF_DEFAULT_MODEL", defaultModel)
	return updates, nil
}

func modelConfigDeletionUpdates(values map[string]string, name string) (map[string]*string, string, error) {
	config := modelgateway.PoolConfigFromLookup(func(key string) string { return values[key] })
	found := false
	for _, model := range config.Models {
		if model.Name == name {
			found = true
			break
		}
	}
	if !found {
		return nil, "", fmt.Errorf("model profile %q was not found", name)
	}

	updates := make(map[string]*string)
	multiModel := strings.TrimSpace(values["CTF_MODELS"]) != ""
	if !multiModel {
		for _, key := range []string{
			"CTF_UPSTREAM_MODEL_BASE_URL", "CTF_UPSTREAM_MODEL_API_KEY", "CTF_MODEL_ID",
			"CTF_MODEL_INCLUDE_STREAM_USAGE", "CTF_MODEL_SUPPORTS_IMAGES", "CTF_DEFAULT_MODEL",
		} {
			updates[key] = nil
		}
		return updates, "", nil
	}

	names := make([]string, 0, len(config.Models)-1)
	for _, model := range config.Models {
		if model.Name != name {
			names = append(names, model.Name)
		}
	}
	for _, suffix := range []string{"BASE_URL", "API_KEY", "ID", "INCLUDE_STREAM_USAGE", "SUPPORTS_IMAGES"} {
		updates[profileKey(name, suffix)] = nil
	}
	nextDefault := config.DefaultModel
	if !containsModelName(names, nextDefault) {
		nextDefault = ""
		if len(names) > 0 {
			nextDefault = names[0]
		}
	}
	if len(names) == 0 {
		updates["CTF_MODELS"] = nil
		updates["CTF_DEFAULT_MODEL"] = nil
		// Legacy keys may remain after a previous one-to-many migration. Clear
		// them so deleting the last profile cannot resurrect a stale default.
		for _, key := range []string{
			"CTF_UPSTREAM_MODEL_BASE_URL", "CTF_UPSTREAM_MODEL_API_KEY", "CTF_MODEL_ID",
			"CTF_MODEL_INCLUDE_STREAM_USAGE", "CTF_MODEL_SUPPORTS_IMAGES",
		} {
			updates[key] = nil
		}
	} else {
		setModelValue(updates, "CTF_MODELS", strings.Join(names, ","))
		setModelValue(updates, "CTF_DEFAULT_MODEL", nextDefault)
	}
	return updates, nextDefault, nil
}

func configuredModel(model modelgateway.ModelConfig) bool {
	return strings.TrimSpace(model.UpstreamBaseURL) != "" && strings.TrimSpace(model.UpstreamAPIKey) != "" && strings.TrimSpace(model.ModelID) != ""
}

func setProfileValues(updates map[string]*string, name string, model modelgateway.ModelConfig) {
	setModelValue(updates, profileKey(name, "BASE_URL"), model.UpstreamBaseURL)
	setModelValue(updates, profileKey(name, "API_KEY"), model.UpstreamAPIKey)
	setModelValue(updates, profileKey(name, "ID"), model.ModelID)
	setModelValue(updates, profileKey(name, "INCLUDE_STREAM_USAGE"), boolString(model.IncludeStreamUsage))
	setModelValue(updates, profileKey(name, "SUPPORTS_IMAGES"), boolString(model.SupportsImages))
}

func profileKey(name, suffix string) string {
	return "CTF_MODEL_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_" + suffix
}

func setModelValue(updates map[string]*string, key, value string) {
	copy := value
	updates[key] = &copy
}

func containsModelName(names []string, candidate string) bool {
	for _, name := range names {
		if name == candidate {
			return true
		}
	}
	return false
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func redactedBaseURL(raw string) string {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return ""
	}
	endpoint.User = nil
	endpoint.RawQuery = ""
	endpoint.ForceQuery = false
	endpoint.Fragment = ""
	return strings.TrimRight(endpoint.String(), "/")
}
