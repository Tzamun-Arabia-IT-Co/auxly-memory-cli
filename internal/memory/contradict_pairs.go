package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/Tzamun-Arabia-IT-Co/auxly-memory-cli/internal/embed"
)

const (
	defaultContradictMaxFacts = 500
	defaultContradictMaxPairs = 50
	defaultContradictMinCos   = 0.80

	// contradictEmbedBatchSize caps each Embed call. embed.Client's request
	// timeout is a fixed per-request budget sized for tiny realtime recall
	// batches (a handful of texts), not a vault-wide sweep — handing it all
	// ≤500 facts in one call blows that budget and surfaces as
	// embed.ErrUnavailable. Chunking keeps every call small enough to fit.
	contradictEmbedBatchSize = 32
)

// ErrVaultTooLarge reports that a contradiction sweep would score too many
// bullets. Callers should surface the message; silently truncating would hide
// exactly the long-tail facts humans need this review to catch.
var ErrVaultTooLarge = errors.New("vault too large for contradiction sweep")

// FactRef identifies one bullet fact in one memory file.
type FactRef struct {
	File   string
	Line   string
	LineNo int
}

// FactPair is a suspiciously-similar cross-file fact pair.
type FactPair struct {
	A, B  FactRef
	Score float32
}

func contradictMaxFacts(defaultMax int) int {
	if v := strings.TrimSpace(os.Getenv("AUXLY_CONTRADICT_MAX_FACTS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMax
}

func isContradictSweepFile(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(baseName(name), ".") || name == unifiedMemoryFile {
		return false
	}
	if IsOrganizableFile(name) {
		return true
	}
	// Keep per-project memory eligible even if the organize gate is tightened
	// later; cross-file project facts are a primary source of stale duplicates.
	rest := strings.TrimPrefix(name, "projects/")
	return rest != name && strings.HasSuffix(rest, ".md") && !strings.Contains(rest, "/")
}

func baseName(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// SimilarCrossFilePairs finds cross-file bullet pairs whose embeddings are close
// enough to deserve an LLM contradiction/duplicate judgment.
func (s *Store) SimilarCrossFilePairs(ctx context.Context, emb Embedder, maxFacts, maxPairs int, minCos float32) ([]FactPair, error) {
	if emb == nil || !emb.Enabled() {
		return nil, fmt.Errorf("contradiction sweep unavailable: %w", embed.ErrUnavailable)
	}
	if maxFacts <= 0 {
		maxFacts = contradictMaxFacts(defaultContradictMaxFacts)
	}
	if maxPairs <= 0 {
		maxPairs = defaultContradictMaxPairs
	}
	if minCos <= 0 {
		minCos = defaultContradictMinCos
	}

	files, err := s.List()
	if err != nil {
		return nil, err
	}

	var facts []FactRef
	for _, f := range files {
		if f.IsDir || IsPersonalFile(f.Name) || !isContradictSweepFile(f.Name) {
			continue
		}
		content, err := s.View(f.Name)
		if err != nil {
			return nil, err
		}
		for lineNo, raw := range strings.Split(content, "\n") {
			line := strings.TrimSpace(raw)
			if !isBulletLine(line) {
				continue
			}
			facts = append(facts, FactRef{File: f.Name, Line: line, LineNo: lineNo + 1})
			if len(facts) > maxFacts {
				return nil, fmt.Errorf("contradiction sweep found %d facts; increase AUXLY_CONTRADICT_MAX_FACTS or pass a higher maxFacts: %w", len(facts), ErrVaultTooLarge)
			}
		}
	}
	if len(facts) < 2 {
		return nil, nil
	}

	texts := make([]string, len(facts))
	for i, f := range facts {
		texts[i] = f.Line
	}
	// Embed in sub-batches (see contradictEmbedBatchSize) rather than one call
	// for all facts — one Embed call per batch, checked against ctx between
	// batches so a cancelled/timed-out caller stops promptly instead of
	// burning through the rest of a large vault.
	vecs := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += contradictEmbedBatchSize {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("contradiction sweep embed failed: %w: %w", err, embed.ErrUnavailable)
		}
		end := i + contradictEmbedBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch, err := emb.Embed(ctx, texts[i:end])
		if err != nil {
			return nil, fmt.Errorf("contradiction sweep embed failed: %w: %w", err, embed.ErrUnavailable)
		}
		vecs = append(vecs, batch...)
	}
	if len(vecs) != len(facts) {
		return nil, fmt.Errorf("contradiction sweep embed count mismatch: %w", embed.ErrUnavailable)
	}

	norms := make([]float64, len(vecs))
	for i, v := range vecs {
		norms[i] = norm(v)
	}

	pairs := make([]FactPair, 0)
	// O(n²) is intentionally simple here: the default cap is 500 facts, so the
	// worst case is ~250k dot products, which is tiny beside the embedding call.
	for i := 0; i < len(facts); i++ {
		for j := i + 1; j < len(facts); j++ {
			if facts[i].File == facts[j].File || len(vecs[i]) == 0 || len(vecs[i]) != len(vecs[j]) {
				continue
			}
			score := cosine(vecs[i], vecs[j], norms[i])
			if score < minCos {
				continue
			}
			pairs = append(pairs, FactPair{A: facts[i], B: facts[j], Score: score})
		}
	}

	sort.Slice(pairs, func(i, j int) bool {
		a, b := pairs[i], pairs[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.A.File != b.A.File {
			return a.A.File < b.A.File
		}
		if a.A.LineNo != b.A.LineNo {
			return a.A.LineNo < b.A.LineNo
		}
		if a.B.File != b.B.File {
			return a.B.File < b.B.File
		}
		return a.B.LineNo < b.B.LineNo
	})
	if len(pairs) > maxPairs {
		pairs = pairs[:maxPairs]
	}
	return pairs, nil
}
