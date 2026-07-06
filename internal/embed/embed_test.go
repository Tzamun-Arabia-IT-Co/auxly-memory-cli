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

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/config"
)

// TestNew_ConfigFallbackAndEnvPrecedence locks the resolution order the MCP
// server depends on: env var > persisted config > built-in default, and that
// the local-vs-cloud gate honours a config-set endpoint + config allow-cloud.
func TestNew_ConfigFallbackAndEnvPrecedence(t *testing.T) {
	resetBreaker()
	t.Cleanup(resetBreaker)
	clearEnv(t) // HOME=temp dir, all embed env cleared

	// No env, no config → built-in local default, enabled.
	if !New().Enabled() {
		t.Fatal("default local endpoint should be enabled")
	}

	// Config LAN endpoint is used when no env is set, and a private IP needs no
	// allow-cloud opt-in.
	if err := config.SaveSettings(config.Settings{
		EmbedEndpoint: "http://192.168.1.141:11434/v1/embeddings",
		EmbedModel:    "nomic-embed-text",
	}); err != nil {
		t.Fatal(err)
	}
	c := New()
	if c.url != "http://192.168.1.141:11434/v1/embeddings" {
		t.Fatalf("config EmbedEndpoint not used: got %q", c.url)
	}
	if c.model != "nomic-embed-text" {
		t.Fatalf("config EmbedModel not used: got %q", c.model)
	}
	if !c.Enabled() {
		t.Fatal("LAN endpoint should be enabled without allow-cloud")
	}

	// Env wins over config.
	t.Setenv("AUXLY_EMBED_ENDPOINT", "http://10.0.0.9:11434/v1/embeddings")
	if got := New().url; got != "http://10.0.0.9:11434/v1/embeddings" {
		t.Fatalf("env must win over config: got %q", got)
	}
	t.Setenv("AUXLY_EMBED_ENDPOINT", "")

	// A PUBLIC config endpoint is disabled until EmbedAllowCloud is set.
	if err := config.SaveSettings(config.Settings{EmbedEndpoint: "https://embeds.example.com/v1/embeddings"}); err != nil {
		t.Fatal(err)
	}
	if New().Enabled() {
		t.Fatal("public config endpoint must be disabled without allow-cloud")
	}
	if err := config.SaveSettings(config.Settings{
		EmbedEndpoint:   "https://embeds.example.com/v1/embeddings",
		EmbedAllowCloud: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !New().Enabled() {
		t.Fatal("EmbedAllowCloud in config should enable a public endpoint")
	}
}

// clearEnv neutralizes every env var that ResolveEndpoint or New consults so a
// test starts from a deterministic "local default" baseline. It also points
// HOME at an empty temp dir so New()'s config.LoadSettings() fallback reads no
// persisted EmbedEndpoint/EmbedModel/EmbedAllowCloud from the real machine.
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
	t.Setenv("HOME", t.TempDir())
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

// TestIsLocalEmbedURL unit-tests the locality classifier directly: loopback,
// RFC-1918 private ranges, link-local, *.local, and empty hosts are local;
// public DNS names and public IPs (and unparseable hosts) are cloud.
func TestIsLocalEmbedURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"localhost", "http://localhost:11434/v1/embeddings", true},
		{"loopback ipv4", "http://127.0.0.1:8000/v1/embeddings", true},
		{"loopback ipv6", "http://[::1]:8000/v1/embeddings", true},
		{"private 192.168", "http://192.168.1.141:8000/v1/embeddings", true},
		{"private 10.x", "http://10.0.0.5:11434/v1/embeddings", true},
		{"private 172.16", "http://172.16.0.3:8000/v1/embeddings", true},
		{"link-local 169.254", "http://169.254.10.10:8000/v1/embeddings", true},
		{"dot local host", "http://foo.local:8080/v1/embeddings", true},
		{"dot localhost host", "http://foo.localhost:8080/v1/embeddings", true},
		{"empty host", "/v1/embeddings", true},
		{"openai cloud", "https://api.openai.com/v1/embeddings", false},
		{"public ip 8.8.8.8", "http://8.8.8.8/v1/embeddings", false},
		{"bare public hostname", "https://some-public-host.example.com/v1/embeddings", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLocalEmbedURL(tt.url); got != tt.want {
				t.Errorf("isLocalEmbedURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

// TestEffectiveURLGate is the FIX-B regression suite: the cloud-exfiltration gate
// must classify the EFFECTIVE embeddings URL (post AUXLY_EMBED_ENDPOINT /
// AUXLY_LLM_BASE / OLLAMA_HOST override), not the resolved LLM provider's IsCloud
// flag. A cloud URL without opt-in must yield Enabled()==false.
func TestEffectiveURLGate(t *testing.T) {
	resetBreaker()
	t.Cleanup(resetBreaker)
	tests := []struct {
		name        string
		env         map[string]string
		wantEnabled bool
	}{
		{
			name:        "embed endpoint openai without opt-in",
			env:         map[string]string{"AUXLY_EMBED_ENDPOINT": "https://api.openai.com/v1/embeddings"},
			wantEnabled: false,
		},
		{
			name:        "embed endpoint openai with opt-in",
			env:         map[string]string{"AUXLY_EMBED_ENDPOINT": "https://api.openai.com/v1/embeddings", "AUXLY_EMBED_ALLOW_CLOUD": "1"},
			wantEnabled: true,
		},
		{
			name:        "llm base public host without opt-in",
			env:         map[string]string{"AUXLY_LLM_BASE": "https://some-public-host.example.com"},
			wantEnabled: false,
		},
		{
			name:        "llm base private host",
			env:         map[string]string{"AUXLY_LLM_BASE": "http://192.168.1.141:8000"},
			wantEnabled: true,
		},
		{
			name:        "ollama host private",
			env:         map[string]string{"OLLAMA_HOST": "http://10.0.0.5:11434"},
			wantEnabled: true,
		},
		{
			name:        "ollama host public without opt-in",
			env:         map[string]string{"OLLAMA_HOST": "https://ollama.example.com"},
			wantEnabled: false,
		},
		{
			name:        "openai api key without opt-in regression",
			env:         map[string]string{"OPENAI_API_KEY": "sk-x"},
			wantEnabled: false,
		},
		{
			name:        "default localhost",
			env:         map[string]string{},
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
			if got := c.Enabled(); got != tt.wantEnabled {
				t.Errorf("Enabled() = %v, want %v (url=%q)", got, tt.wantEnabled, c.url)
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
