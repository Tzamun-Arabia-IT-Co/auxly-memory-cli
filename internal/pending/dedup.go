package pending

import "strings"

// linesEquivalent decides whether an incoming addition duplicates an existing
// memory line. Exact trimmed equality was too fragile — "John's married." vs
// "john is married" both survived. Two tiers, stdlib only:
//  1. normalized equality (case, punctuation, whitespace runs);
//  2. character-trigram Dice similarity ≥ 0.9 on the normalized forms, for
//     near-duplicates with tiny wording drift. Short lines skip tier 2 —
//     trigram similarity is unstable below ~20 chars and would false-merge
//     distinct short facts.
//
// Deletions deliberately still match EXACTLY (see ApplyDiff): fuzzily deleting
// the wrong fact is data loss; fuzzily skipping a duplicate is harmless.
func linesEquivalent(a, b string) bool {
	return normLinesEquivalent(normalizeLine(a), normalizeLine(b))
}

// normLinesEquivalent is linesEquivalent over PRE-normalized inputs, so batch
// callers (ApplyDiff) normalize each line once instead of per comparison.
// Numbers are the identity of a fact (IPs, amounts, dates, versions, case
// numbers): ANY digit difference disqualifies equivalence on BOTH tiers —
// "v1.2" vs "v12" and "…1.24" vs "…1.25" are different facts.
func normLinesEquivalent(na, nb string) bool {
	if digitSignature(na) != digitSignature(nb) {
		return false
	}
	if na == nb {
		return true
	}
	if len(na) < 20 || len(nb) < 20 {
		return false
	}
	return trigramSimilarity(na, nb) >= 0.9
}

// digitSignature captures the sequence of digit RUNS (with boundaries), not a
// flat digit concatenation: "12.1.2024" → "12|1|2024" vs "1.21.2024" →
// "1|21|2024" must differ even though their digits concatenate identically.
func digitSignature(s string) string {
	var b strings.Builder
	prevDigit := false
	for _, r := range s {
		isDigit := r >= '0' && r <= '9'
		if isDigit {
			b.WriteRune(r)
		} else if prevDigit {
			b.WriteRune('|')
		}
		prevDigit = isDigit
	}
	return b.String()
}

// normalizeLine lowercases, replaces punctuation with a SPACE (never deletes
// it — deleting would fuse digit runs so "v1.2" == "v12"), collapses
// whitespace, and maps Arabic-Indic digits to ASCII so digitSignature sees
// numbers regardless of script.
func normalizeLine(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 0x0660 && r <= 0x0669: // Eastern Arabic-Indic digits ٠-٩
			b.WriteRune('0' + (r - 0x0660))
		case r >= 0x06F0 && r <= 0x06F9: // Extended Arabic-Indic digits ۰-۹
			b.WriteRune('0' + (r - 0x06F0))
		case r > 127: // keep non-ASCII letters (Arabic etc.) — only ASCII punctuation is noise
			b.WriteRune(r)
		default: // ASCII punctuation/whitespace → separator, preserved as a boundary
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// trigramSimilarity is the Dice coefficient over the two strings' character
// trigram sets: 2·|A∩B| / (|A|+|B|).
func trigramSimilarity(a, b string) float64 {
	ta, tb := trigrams(a), trigrams(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	inter := 0
	for g := range ta {
		if tb[g] {
			inter++
		}
	}
	return 2 * float64(inter) / float64(len(ta)+len(tb))
}

func trigrams(s string) map[string]bool {
	r := []rune(s)
	out := make(map[string]bool, len(r))
	for i := 0; i+3 <= len(r); i++ {
		out[string(r[i:i+3])] = true
	}
	return out
}
