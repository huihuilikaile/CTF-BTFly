package modelgateway

import (
	"context"
	"net/http"
	"strings"
)

// Manager 统一旧单模型网关与多模型池对任务服务、HTTP API 的能力契约。
type Manager interface {
	http.Handler
	Configured() bool
	DefaultProfile() string
	Profiles() []ModelStatus
	Profile(string) (ModelStatus, bool)
	ModelID(...string) string
	SupportsImages(...string) bool
	Probe(context.Context, ...string) error
	ProbeStatus(...string) ProbeStatus
	Issue(string, ...string) (string, error)
	Revoke(string)
	SetUsageRecorder(UsageRecorder)
	SetErrorReporter(ErrorReporter)
}

// DefaultProfile 为旧单模型配置提供稳定名称。
func (g *Gateway) DefaultProfile() string { return "default" }

func (g *Gateway) Profiles() []ModelStatus {
	status, _ := g.Profile("")
	return []ModelStatus{status}
}

func (g *Gateway) Profile(name string) (ModelStatus, bool) {
	if name != "" && name != "default" {
		return ModelStatus{}, false
	}
	return ModelStatus{Name: "default", ModelID: g.config.ModelID, BaseURL: displayBaseURL(g.config.UpstreamBaseURL), Configured: g.Configured(), HasAPIKey: strings.TrimSpace(g.config.UpstreamAPIKey) != "", SupportsImages: g.config.SupportsImages, IncludeStreamUsage: g.config.IncludeStreamUsage, Default: true, Probe: g.ProbeStatus()}, true
}
