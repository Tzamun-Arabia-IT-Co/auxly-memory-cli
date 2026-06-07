package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Chunk is a human-meaningful unit of a Markdown memory file: a bullet group or
// paragraph under its nearest heading, mapped back to a 1-based line range and
// fingerprinted with a content hash for incremental re-embedding.
type Chunk struct {
	File      string // the source file path/identifier passed in
	Heading   string // nearest preceding markdown heading text (no leading #), "" if none yet
	Text      string // the chunk's text content (trimmed)
	LineStart int    // 1-based line number of the chunk's first line in the file
	LineEnd   int    // 1-based line number of the chunk's last line
	Hash      string // sha256 hex of Text, for incremental re-embed
}

// ChunkMarkdown splits markdown content into chunks by heading + bullet/paragraph
// group. Headings set context for the chunks under them; consecutive non-blank
// lines form one chunk, and a blank line, a new heading, or EOF closes it.
func ChunkMarkdown(file, content string) []Chunk {
	lines := strings.Split(content, "\n")

	var chunks []Chunk
	currentHeading := ""

	// Pending group accumulator: raw line text plus its 1-based start line.
	var group []string
	groupStart := 0

	flush := func() {
		if len(group) == 0 {
			return
		}
		text := strings.TrimRight(strings.Join(group, "\n"), " \t\n")
		if text != "" {
			chunks = append(chunks, Chunk{
				File:      file,
				Heading:   currentHeading,
				Text:      text,
				LineStart: groupStart,
				LineEnd:   groupStart + len(group) - 1,
				Hash:      hashText(text),
			})
		}
		group = nil
		groupStart = 0
	}

	for i, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		lineNo := i + 1

		if heading, ok := parseHeading(line); ok {
			flush()
			currentHeading = heading
			continue
		}

		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}

		if len(group) == 0 {
			groupStart = lineNo
		}
		group = append(group, line)
	}
	flush()

	return chunks
}

// parseHeading returns the heading text (no leading #s or surrounding whitespace)
// when line matches `^#{1,6}\s+...`, and false otherwise.
func parseHeading(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	hashes := 0
	for hashes < len(trimmed) && trimmed[hashes] == '#' {
		hashes++
	}
	if hashes < 1 || hashes > 6 || hashes >= len(trimmed) {
		return "", false
	}
	rest := trimmed[hashes:]
	if rest[0] != ' ' && rest[0] != '\t' {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// hashText returns the lowercase sha256 hex digest of s.
func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
