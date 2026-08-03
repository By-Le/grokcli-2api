package minimax

import "strings"

const (
	ProviderName = "MiniMax"
	DefaultModel = "MiniMax-M3"
	ModelM27     = "MiniMax-M2.7"

	RegionGlobalEN = "global_en"
	RegionCNZH     = "cn_zh"
)

type Endpoints struct {
	OpenAIBaseURL    string
	AnthropicBaseURL string
}

type modelSpec struct {
	id             string
	contextWindow  int
	inputPrice     float64
	outputPrice    float64
	cacheReadPrice float64
	cacheWrite     *float64
	modalities     []string
	thinking       []string
}

var modelSpecs = []modelSpec{
	{
		id:             DefaultModel,
		contextWindow:  1_000_000,
		inputPrice:     0.6,
		outputPrice:    2.4,
		cacheReadPrice: 0.12,
		modalities:     []string{"text", "image", "video"},
		thinking:       []string{"adaptive", "disabled"},
	},
	{
		id:             ModelM27,
		contextWindow:  204_800,
		inputPrice:     0.3,
		outputPrice:    1.2,
		cacheReadPrice: 0.06,
		cacheWrite:     float64Ptr(0.375),
		modalities:     []string{"text"},
		thinking:       []string{"always_on"},
	},
}

func EndpointsForRegion(region string) (Endpoints, bool) {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "", RegionGlobalEN:
		return Endpoints{
			OpenAIBaseURL:    "https://api.minimax.io/v1",
			AnthropicBaseURL: "https://api.minimax.io/anthropic",
		}, true
	case RegionCNZH:
		return Endpoints{
			OpenAIBaseURL:    "https://api.minimaxi.com/v1",
			AnthropicBaseURL: "https://api.minimaxi.com/anthropic",
		}, true
	default:
		return Endpoints{}, false
	}
}

func ModelIDs() []string {
	return []string{DefaultModel, ModelM27}
}

func IsModel(model string) bool {
	switch ResolveModel(model, DefaultModel) {
	case DefaultModel, ModelM27:
		return true
	default:
		return false
	}
}

func ResolveModel(model, fallback string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return canonicalFallback(fallback)
	}
	switch strings.ToLower(model) {
	case "minimax", "minimax-latest", strings.ToLower(DefaultModel):
		return DefaultModel
	case strings.ToLower(ModelM27):
		return ModelM27
	default:
		return model
	}
}

func CatalogEntries(created int64) []map[string]any {
	entries := make([]map[string]any, 0, len(modelSpecs))
	for _, spec := range modelSpecs {
		var cacheWrite any
		if spec.cacheWrite != nil {
			cacheWrite = *spec.cacheWrite
		}
		entries = append(entries, map[string]any{
			"id":             spec.id,
			"name":           spec.id,
			"object":         "model",
			"created":        created,
			"owned_by":       ProviderName,
			"context_window": spec.contextWindow,
			"pricing_usd_per_million_tokens": map[string]any{
				"input":       spec.inputPrice,
				"output":      spec.outputPrice,
				"cache_read":  spec.cacheReadPrice,
				"cache_write": cacheWrite,
			},
			"input_modalities": append([]string(nil), spec.modalities...),
			"thinking":         append([]string(nil), spec.thinking...),
		})
	}
	return entries
}

func canonicalFallback(fallback string) string {
	switch strings.ToLower(strings.TrimSpace(fallback)) {
	case strings.ToLower(ModelM27):
		return ModelM27
	default:
		return DefaultModel
	}
}

func float64Ptr(value float64) *float64 {
	return &value
}
