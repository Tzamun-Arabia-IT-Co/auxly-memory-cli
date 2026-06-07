// Package llm resolves an OpenAI-compatible LLM endpoint (base URL, auth, and
// default model) from the environment. The resolution precedence is shared by
// callers that need chat completions, embeddings, or model listing against the
// same provider.
package llm

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

// probeTimeout caps the loopback :8000 probe and the model self-heal lookup.
// Timeouts for the actual request/response belong to callers, not this package.
const probeTimeout = 800 * time.Millisecond

const defaultLocalModel = "qwen2.5-coder:7b"

// Endpoint describes a resolved OpenAI-compatible LLM endpoint.
type Endpoint struct {
	APIBase string // e.g. "http://localhost:11434/v1" or gemini's "/v1beta/openai" base
	APIKey  string // empty for local providers
	IsCloud bool   // true ONLY for OpenAI and Gemini (managed SaaS)
	Source  string // "openai" | "gemini" | "ollama_host" | "auxly_llm_base" | "probe_8000" | "ollama_default"
	Model   string // the default chat model for this provider
}

// ChatURL returns the chat completions endpoint for this provider.
func (e Endpoint) ChatURL() string { return e.APIBase + "/chat/completions" }

// EmbeddingsURL returns the embeddings endpoint for this provider.
func (e Endpoint) EmbeddingsURL() string { return e.APIBase + "/embeddings" }

// ModelsURL returns the model-listing endpoint for this provider.
func (e Endpoint) ModelsURL() string { return e.APIBase + "/models" }

// ResolveEndpoint replicates organize.go's precedence exactly:
//  1. OPENAI_API_KEY  -> OpenAI cloud
//  2. GEMINI_API_KEY  -> Gemini cloud (irregular /v1beta/openai base)
//  3. OLLAMA_HOST     -> $OLLAMA_HOST/v1 (local)
//  4. AUXLY_LLM_BASE  -> $AUXLY_LLM_BASE/v1 (trailing slash trimmed, local)
//  5. probe http://localhost:8000/v1/models (loopback, 800ms) -> :8000 (local)
//  6. default Ollama  -> http://localhost:11434/v1 (local)
func ResolveEndpoint() Endpoint {
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		return Endpoint{
			APIBase: "https://api.openai.com/v1",
			APIKey:  key,
			IsCloud: true,
			Source:  "openai",
			Model:   "gpt-4o-mini",
		}
	}

	if key := os.Getenv("GEMINI_API_KEY"); key != "" {
		return Endpoint{
			APIBase: "https://generativelanguage.googleapis.com/v1beta/openai",
			APIKey:  key,
			IsCloud: true,
			Source:  "gemini",
			Model:   "gemini-1.5-flash",
		}
	}

	if host := os.Getenv("OLLAMA_HOST"); host != "" {
		return Endpoint{
			APIBase: host + "/v1",
			Source:  "ollama_host",
			Model:   defaultLocalModel,
		}
	}

	if base := strings.TrimRight(os.Getenv("AUXLY_LLM_BASE"), "/"); base != "" {
		return Endpoint{
			APIBase: base + "/v1",
			Source:  "auxly_llm_base",
			Model:   defaultLocalModel,
		}
	}

	// Last resort: probe a local OpenAI-compatible server on the conventional
	// localhost port (vLLM / LM Studio default). Stays on the loopback
	// interface — never reaches out to the network.
	client := &http.Client{Timeout: probeTimeout}
	if resp, err := client.Get("http://localhost:8000/v1/models"); err == nil {
		resp.Body.Close()
		return Endpoint{
			APIBase: "http://localhost:8000/v1",
			Source:  "probe_8000",
			Model:   defaultLocalModel,
		}
	}

	return Endpoint{
		APIBase: "http://localhost:11434/v1",
		Source:  "ollama_default",
		Model:   defaultLocalModel,
	}
}

// SelfHealModel queries modelsURL (800ms) and returns the first served model id
// (data[0].id) when present, preventing 404s on Ollama/vLLM where the configured
// default model may not be installed. On any error or empty list it returns the
// supplied fallback unchanged.
func SelfHealModel(modelsURL, fallback string) string {
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Get(modelsURL)
	if err != nil {
		return fallback
	}
	defer resp.Body.Close()

	type modelInfo struct {
		ID string `json:"id"`
	}
	type modelsResp struct {
		Data []modelInfo `json:"data"`
	}
	var mr modelsResp
	if err := json.NewDecoder(resp.Body).Decode(&mr); err == nil && len(mr.Data) > 0 {
		return mr.Data[0].ID
	}
	return fallback
}
