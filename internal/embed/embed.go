// Package embed turns text into embedding vectors via an OpenAI-compatible
// /v1/embeddings endpoint. It reuses internal/llm's endpoint resolver but adds a
// strict privacy gate: embedding the user's entire memory vault must never be
// silently uploaded to a cloud provider. A resolved cloud endpoint is disabled
// unless AUXLY_EMBED_ALLOW_CLOUD=1 is set, so the default is always local-only.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/llm"
)

// ErrUnavailable signals the caller to fall back to substring search. Returned
// when no endpoint resolves, a cloud endpoint lacks opt-in, or a request
// fails/times out. It is NOT a user-facing error.
var ErrUnavailable = errors.New("embedding endpoint unavailable")

// requestTimeout caps both the request context and the HTTP client so a slow or
// unreachable endpoint never stalls a vault-wide embedding pass.
const requestTimeout = 500 * time.Millisecond

const (
	cloudEmbedModel = "text-embedding-3-small"
	localEmbedModel = "nomic-embed-text"
)

// Client embeds text against a resolved OpenAI-compatible endpoint, subject to
// the local-only privacy gate.
type Client struct {
	url      string // embeddings URL (override or endpoint.EmbeddingsURL())
	apiKey   string
	model    string
	provider string
	enabled  bool
}

// New resolves the endpoint and applies the cloud gate. When the per-process
// circuit breaker is open it returns a disabled client immediately, skipping the
// (potentially 800ms) endpoint probe in llm.ResolveEndpoint entirely.
func New() *Client {
	if breakerOpen() {
		return &Client{enabled: false}
	}

	endpoint := llm.ResolveEndpoint()

	model := os.Getenv("AUXLY_EMBED_MODEL")
	if model == "" {
		if endpoint.IsCloud {
			model = cloudEmbedModel
		} else {
			model = localEmbedModel
		}
	}

	url := os.Getenv("AUXLY_EMBED_ENDPOINT")
	if url == "" {
		url = endpoint.EmbeddingsURL()
	}

	enabled := !endpoint.IsCloud || os.Getenv("AUXLY_EMBED_ALLOW_CLOUD") == "1"

	return &Client{
		url:      url,
		apiKey:   endpoint.APIKey,
		model:    model,
		provider: endpoint.Source,
		enabled:  enabled,
	}
}

// Enabled reports whether embedding is permitted: false when the resolved
// endpoint is cloud-backed without an explicit opt-in.
func (c *Client) Enabled() bool { return c.enabled }

// Model returns the resolved embedding model id.
func (c *Client) Model() string { return c.model }

// Provider returns the resolved endpoint source (for index metadata).
func (c *Client) Provider() string { return c.provider }

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedData struct {
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

type embedResponse struct {
	Data []embedData `json:"data"`
}

// Embed returns one vector per input text, ordered to match the inputs. It
// returns ErrUnavailable (wrapped) when the client is disabled or the request
// fails, errors, or times out within 500ms.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if !c.enabled {
		return nil, fmt.Errorf("embedding disabled (cloud endpoint without opt-in): %w", ErrUnavailable)
	}

	// Fail fast while the breaker is open: skip the HTTP attempt entirely.
	if breakerOpen() {
		return nil, fmt.Errorf("embedding circuit breaker open: %w", ErrUnavailable)
	}

	body, err := json.Marshal(embedRequest{Model: c.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w: %w", err, ErrUnavailable)
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build embed request: %w: %w", err, ErrUnavailable)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		breakerRecordFailure()
		return nil, fmt.Errorf("embed request failed: %w: %w", err, ErrUnavailable)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		breakerRecordFailure()
		return nil, fmt.Errorf("embed request status %d: %w", resp.StatusCode, ErrUnavailable)
	}

	var parsed embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		breakerRecordFailure()
		return nil, fmt.Errorf("decode embed response: %w: %w", err, ErrUnavailable)
	}
	if len(parsed.Data) == 0 {
		breakerRecordFailure()
		return nil, fmt.Errorf("embed response had no data: %w", ErrUnavailable)
	}

	// Order by the index field so vectors line up with the input order even if
	// the provider returns them out of sequence.
	sort.Slice(parsed.Data, func(i, j int) bool {
		return parsed.Data[i].Index < parsed.Data[j].Index
	})

	vectors := make([][]float32, len(parsed.Data))
	for i, d := range parsed.Data {
		vec := make([]float32, len(d.Embedding))
		for j, v := range d.Embedding {
			vec[j] = float32(v)
		}
		vectors[i] = vec
	}
	breakerRecordSuccess()
	return vectors, nil
}
