package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// clearEnv neutralizes every env var that ResolveEndpoint or New consults so a
// test starts from a deterministic "local default" baseline.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OPENAI_API_KEY",
		"GEMINI_API_KEY",
		"OLLAMA_HOST",
		"AUXLY_LLM_BASE",
		"AUXLY_EMBED_MODEL",
		"AUXLY_EMBED_ALLOW_CLOUD",
		"AUXLY_EMBED_ENDPOINT",
	} {
		t.Setenv(k, "")
	}
}

func TestNewModelAndEnabled(t *testing.T) {
	resetBreaker()
	t.Cleanup(resetBreaker)
	tests := []struct {
		name        string
		env         map[string]string
		wantModel   string
		wantEnabled bool
	}{
		{
			name:        "local default model",
			env:         map[string]string{},
			wantModel:   "nomic-embed-text",
			wantEnabled: true,
		},
		{
			name:        "cloud blocked without opt-in",
			env:         map[string]string{"OPENAI_API_KEY": "sk-x"},
			wantModel:   "text-embedding-3-small",
			wantEnabled: false,
		},
		{
			name:        "cloud allowed with opt-in",
			env:         map[string]string{"OPENAI_API_KEY": "sk-x", "AUXLY_EMBED_ALLOW_CLOUD": "1"},
			wantModel:   "text-embedding-3-small",
			wantEnabled: true,
		},
		{
			name:        "embed model override wins local",
			env:         map[string]string{"AUXLY_EMBED_MODEL": "custom-embed"},
			wantModel:   "custom-embed",
			wantEnabled: true,
		},
		{
			name:        "embed model override wins cloud",
			env:         map[string]string{"OPENAI_API_KEY": "sk-x", "AUXLY_EMBED_ALLOW_CLOUD": "1", "AUXLY_EMBED_MODEL": "custom-embed"},
			wantModel:   "custom-embed",
			wantEnabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			c := New()
			if got := c.Model(); got != tt.wantModel {
				t.Errorf("Model() = %q, want %q", got, tt.wantModel)
			}
			if got := c.Enabled(); got != tt.wantEnabled {
				t.Errorf("Enabled() = %v, want %v", got, tt.wantEnabled)
			}
		})
	}
}

func TestProviderLocalDefault(t *testing.T) {
	resetBreaker()
	t.Cleanup(resetBreaker)
	clearEnv(t)
	c := New()
	if got := c.Provider(); got != "ollama_default" {
		t.Errorf("Provider() = %q, want %q", got, "ollama_default")
	}
}

// TestCloudBlockedPrivacy is the critical privacy test: a cloud endpoint without
// the explicit opt-in must NOT embed (would upload the vault). Embed must report
// ErrUnavailable and return no vectors.
func TestCloudBlockedPrivacy(t *testing.T) {
	resetBreaker()
	t.Cleanup(resetBreaker)
	clearEnv(t)
	t.Setenv("OPENAI_API_KEY", "sk-x")

	c := New()
	if c.Enabled() {
		t.Fatal("Enabled() = true for cloud endpoint without opt-in; privacy gate FAILED")
	}

	vecs, err := c.Embed(context.Background(), []string{"hi"})
	if vecs != nil {
		t.Errorf("Embed returned vectors %v, want nil when cloud-blocked", vecs)
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Embed error = %v, want errors.Is ErrUnavailable", err)
	}
}

func TestEmbedSuccess(t *testing.T) {
	resetBreaker()
	t.Cleanup(resetBreaker)
	clearEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return data deliberately out of order to prove index-sorting.
		resp := map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{3, 4, 5}, "index": 1},
				{"embedding": []float64{0, 1, 2}, "index": 0},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	t.Setenv("AUXLY_EMBED_ENDPOINT", srv.URL)

	c := New()
	if !c.Enabled() {
		t.Fatal("Enabled() = false, want true for local endpoint")
	}

	got, err := c.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed error = %v, want nil", err)
	}
	want := [][]float32{{0, 1, 2}, {3, 4, 5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Embed() = %v, want %v", got, want)
	}
}

func TestEmbedUnreachableNon200Empty(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		// useBadURL points the client at a closed server when true.
		useBadURL bool
	}{
		{
			name:      "unreachable",
			useBadURL: true,
		},
		{
			name: "non-200",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
		},
		{
			name: "empty data",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetBreaker()
			t.Cleanup(resetBreaker)
			clearEnv(t)

			var url string
			if tt.useBadURL {
				// A closed server: create then immediately close.
				dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				url = dead.URL
				dead.Close()
			} else {
				srv := httptest.NewServer(tt.handler)
				defer srv.Close()
				url = srv.URL
			}
			t.Setenv("AUXLY_EMBED_ENDPOINT", url)

			c := New()
			vecs, err := c.Embed(context.Background(), []string{"hi"})
			if vecs != nil {
				t.Errorf("Embed returned vectors %v, want nil on failure", vecs)
			}
			if !errors.Is(err, ErrUnavailable) {
				t.Errorf("Embed error = %v, want errors.Is ErrUnavailable", err)
			}
		})
	}
}

func TestEmbedTimeout(t *testing.T) {
	resetBreaker()
	t.Cleanup(resetBreaker)
	clearEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float64{1}, "index": 0}},
		})
	}))
	defer srv.Close()

	t.Setenv("AUXLY_EMBED_ENDPOINT", srv.URL)

	c := New()

	start := time.Now()
	vecs, err := c.Embed(context.Background(), []string{"hi"})
	elapsed := time.Since(start)

	if elapsed > 900*time.Millisecond {
		t.Errorf("Embed took %v, want it to bail out around the 500ms timeout", elapsed)
	}
	if vecs != nil {
		t.Errorf("Embed returned vectors %v, want nil on timeout", vecs)
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Embed error = %v, want errors.Is ErrUnavailable", err)
	}
}
