package llm

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveEndpoint(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		wantBase   string
		wantKey    string
		wantCloud  bool
		wantSource string
		wantModel  string
	}{
		{
			name:       "openai",
			env:        map[string]string{"OPENAI_API_KEY": "sk-openai"},
			wantBase:   "https://api.openai.com/v1",
			wantKey:    "sk-openai",
			wantCloud:  true,
			wantSource: "openai",
			wantModel:  "gpt-4o-mini",
		},
		{
			name:       "gemini",
			env:        map[string]string{"GEMINI_API_KEY": "g-key"},
			wantBase:   "https://generativelanguage.googleapis.com/v1beta/openai",
			wantKey:    "g-key",
			wantCloud:  true,
			wantSource: "gemini",
			wantModel:  "gemini-1.5-flash",
		},
		{
			name:       "ollama_host",
			env:        map[string]string{"OLLAMA_HOST": "http://example:11434"},
			wantBase:   "http://example:11434/v1",
			wantKey:    "",
			wantCloud:  false,
			wantSource: "ollama_host",
			wantModel:  "qwen2.5-coder:7b",
		},
		{
			name:       "auxly_llm_base",
			env:        map[string]string{"AUXLY_LLM_BASE": "http://gw.local:9000"},
			wantBase:   "http://gw.local:9000/v1",
			wantKey:    "",
			wantCloud:  false,
			wantSource: "auxly_llm_base",
			wantModel:  "qwen2.5-coder:7b",
		},
		{
			name:       "auxly_llm_base_trailing_slash",
			env:        map[string]string{"AUXLY_LLM_BASE": "http://gw.local:9000/"},
			wantBase:   "http://gw.local:9000/v1",
			wantKey:    "",
			wantCloud:  false,
			wantSource: "auxly_llm_base",
			wantModel:  "qwen2.5-coder:7b",
		},
		{
			name:       "openai_wins_over_gemini",
			env:        map[string]string{"OPENAI_API_KEY": "sk-openai", "GEMINI_API_KEY": "g-key"},
			wantBase:   "https://api.openai.com/v1",
			wantKey:    "sk-openai",
			wantCloud:  true,
			wantSource: "openai",
			wantModel:  "gpt-4o-mini",
		},
		{
			name:       "ollama_host_wins_over_auxly_llm_base",
			env:        map[string]string{"OLLAMA_HOST": "http://example:11434", "AUXLY_LLM_BASE": "http://gw.local:9000"},
			wantBase:   "http://example:11434/v1",
			wantKey:    "",
			wantCloud:  false,
			wantSource: "ollama_host",
			wantModel:  "qwen2.5-coder:7b",
		},
	}

	// All env vars the resolver reads — clear them so cases stay isolated.
	allKeys := []string{"OPENAI_API_KEY", "GEMINI_API_KEY", "OLLAMA_HOST", "AUXLY_LLM_BASE"}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range allKeys {
				t.Setenv(k, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			got := ResolveEndpoint()
			if got.APIBase != tc.wantBase {
				t.Errorf("APIBase = %q, want %q", got.APIBase, tc.wantBase)
			}
			if got.APIKey != tc.wantKey {
				t.Errorf("APIKey = %q, want %q", got.APIKey, tc.wantKey)
			}
			if got.IsCloud != tc.wantCloud {
				t.Errorf("IsCloud = %v, want %v", got.IsCloud, tc.wantCloud)
			}
			if got.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tc.wantSource)
			}
			if got.Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", got.Model, tc.wantModel)
			}
		})
	}
}

func TestResolveEndpointDefaultProbe(t *testing.T) {
	// With no env vars set, the resolver either probes :8000 (if up) or falls
	// back to the default Ollama endpoint. Both are acceptable — keep non-flaky.
	for _, k := range []string{"OPENAI_API_KEY", "GEMINI_API_KEY", "OLLAMA_HOST", "AUXLY_LLM_BASE"} {
		t.Setenv(k, "")
	}

	got := ResolveEndpoint()
	if got.Source != "probe_8000" && got.Source != "ollama_default" {
		t.Errorf("Source = %q, want probe_8000 or ollama_default", got.Source)
	}
	if got.IsCloud {
		t.Errorf("IsCloud = true, want false for local default")
	}
	if got.APIKey != "" {
		t.Errorf("APIKey = %q, want empty for local default", got.APIKey)
	}
	switch got.Source {
	case "probe_8000":
		if got.APIBase != "http://localhost:8000/v1" {
			t.Errorf("APIBase = %q, want http://localhost:8000/v1", got.APIBase)
		}
	case "ollama_default":
		if got.APIBase != "http://localhost:11434/v1" {
			t.Errorf("APIBase = %q, want http://localhost:11434/v1", got.APIBase)
		}
		if got.Model != "qwen2.5-coder:7b" {
			t.Errorf("Model = %q, want qwen2.5-coder:7b", got.Model)
		}
	}
}

func TestEndpointURLs(t *testing.T) {
	normal := Endpoint{APIBase: "http://localhost:11434/v1"}
	if got := normal.ChatURL(); got != "http://localhost:11434/v1/chat/completions" {
		t.Errorf("ChatURL = %q", got)
	}
	if got := normal.EmbeddingsURL(); got != "http://localhost:11434/v1/embeddings" {
		t.Errorf("EmbeddingsURL = %q", got)
	}
	if got := normal.ModelsURL(); got != "http://localhost:11434/v1/models" {
		t.Errorf("ModelsURL = %q", got)
	}

	gemini := Endpoint{APIBase: "https://generativelanguage.googleapis.com/v1beta/openai"}
	if got := gemini.ChatURL(); got != "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions" {
		t.Errorf("gemini ChatURL = %q", got)
	}
	if got := gemini.EmbeddingsURL(); got != "https://generativelanguage.googleapis.com/v1beta/openai/embeddings" {
		t.Errorf("gemini EmbeddingsURL = %q", got)
	}
	if got := gemini.ModelsURL(); got != "https://generativelanguage.googleapis.com/v1beta/openai/models" {
		t.Errorf("gemini ModelsURL = %q", got)
	}
}

func TestSelfHealModel(t *testing.T) {
	t.Run("returns served model when data present", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"data":[{"id":"served-model"}]}`))
		}))
		defer srv.Close()

		got := SelfHealModel(srv.URL, "fallback")
		if got != "served-model" {
			t.Errorf("SelfHealModel = %q, want served-model", got)
		}
	})

	t.Run("returns fallback when data empty", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"data":[]}`))
		}))
		defer srv.Close()

		got := SelfHealModel(srv.URL, "fallback")
		if got != "fallback" {
			t.Errorf("SelfHealModel = %q, want fallback", got)
		}
	})

	t.Run("returns fallback when unreachable", func(t *testing.T) {
		// Closed server → connection refused.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close()

		got := SelfHealModel(url, "fallback")
		if got != "fallback" {
			t.Errorf("SelfHealModel = %q, want fallback", got)
		}
	})
}
