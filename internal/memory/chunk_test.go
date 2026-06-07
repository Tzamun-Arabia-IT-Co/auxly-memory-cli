package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// hashOf is a local helper mirroring the production hashing so tests assert the
// contract (sha256 hex of Text) rather than a magic constant.
func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestChunkMarkdown_HeadingAssociation(t *testing.T) {
	// Arrange
	content := "## Project X\n- bullet a\n- bullet b\n\n## Project Y\n- bullet c"

	// Act
	chunks := ChunkMarkdown("projects.md", content)

	// Assert
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d: %+v", len(chunks), chunks)
	}
	if chunks[0].Heading != "Project X" {
		t.Errorf("chunk 0 heading = %q, want %q", chunks[0].Heading, "Project X")
	}
	if chunks[0].Text != "- bullet a\n- bullet b" {
		t.Errorf("chunk 0 text = %q, want %q", chunks[0].Text, "- bullet a\n- bullet b")
	}
	if chunks[1].Heading != "Project Y" {
		t.Errorf("chunk 1 heading = %q, want %q", chunks[1].Heading, "Project Y")
	}
	if chunks[1].Text != "- bullet c" {
		t.Errorf("chunk 1 text = %q, want %q", chunks[1].Text, "- bullet c")
	}
	for i, c := range chunks {
		if c.File != "projects.md" {
			t.Errorf("chunk %d file = %q, want projects.md", i, c.File)
		}
	}
}

func TestChunkMarkdown_LineMapping(t *testing.T) {
	// Arrange — bullets on lines 2-3 under a heading on line 1
	content := "## Heading\n- bullet a\n- bullet b"

	// Act
	chunks := ChunkMarkdown("f.md", content)

	// Assert
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].LineStart != 2 {
		t.Errorf("LineStart = %d, want 2", chunks[0].LineStart)
	}
	if chunks[0].LineEnd != 3 {
		t.Errorf("LineEnd = %d, want 3", chunks[0].LineEnd)
	}
}

func TestChunkMarkdown_HashStability(t *testing.T) {
	// Arrange
	content := "## H\n- one\n\n## H2\n- two"

	// Act
	a := ChunkMarkdown("f.md", content)
	b := ChunkMarkdown("f.md", content)

	// Assert — identical input yields identical hashes
	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("expected 2 chunks each, got %d and %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Hash != b[i].Hash {
			t.Errorf("chunk %d hash unstable: %q vs %q", i, a[i].Hash, b[i].Hash)
		}
		if a[i].Hash != hashOf(a[i].Text) {
			t.Errorf("chunk %d hash %q != sha256(text)", i, a[i].Hash)
		}
	}

	// Changing one character in the first chunk's text changes only its hash.
	changed := ChunkMarkdown("f.md", "## H\n- oneX\n\n## H2\n- two")
	if changed[0].Hash == a[0].Hash {
		t.Error("changed chunk 0 should have a different hash")
	}
	if changed[1].Hash != a[1].Hash {
		t.Errorf("unchanged chunk 1 hash should stay %q, got %q", a[1].Hash, changed[1].Hash)
	}
}

func TestChunkMarkdown_BlankLineGrouping(t *testing.T) {
	// Arrange — two bullet groups separated by a blank line, same heading
	content := "## Group\n- a\n- b\n\n- c\n- d"

	// Act
	chunks := ChunkMarkdown("f.md", content)

	// Assert
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d: %+v", len(chunks), chunks)
	}
	if chunks[0].Heading != "Group" || chunks[1].Heading != "Group" {
		t.Errorf("both chunks should have heading Group, got %q and %q",
			chunks[0].Heading, chunks[1].Heading)
	}
	if chunks[0].Text != "- a\n- b" {
		t.Errorf("chunk 0 text = %q", chunks[0].Text)
	}
	if chunks[1].Text != "- c\n- d" {
		t.Errorf("chunk 1 text = %q", chunks[1].Text)
	}
}

func TestChunkMarkdown_NoHeadingYet(t *testing.T) {
	// Arrange — bullets before any heading
	content := "- a\n- b\n\n## Later\n- c"

	// Act
	chunks := ChunkMarkdown("f.md", content)

	// Assert
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0].Heading != "" {
		t.Errorf("chunk 0 heading = %q, want empty", chunks[0].Heading)
	}
	if chunks[1].Heading != "Later" {
		t.Errorf("chunk 1 heading = %q, want Later", chunks[1].Heading)
	}
}

func TestChunkMarkdown_EmptyContent(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\n", " \n\t\n "} {
		if got := ChunkMarkdown("f.md", in); len(got) != 0 {
			t.Errorf("ChunkMarkdown(%q) = %d chunks, want 0", in, len(got))
		}
	}
}

func TestChunkMarkdown_CRLF(t *testing.T) {
	// Arrange — same content, CRLF vs LF endings
	lf := "## H\n- a\n- b\n\n## H2\n- c"
	crlf := "## H\r\n- a\r\n- b\r\n\r\n## H2\r\n- c"

	// Act
	gotLF := ChunkMarkdown("f.md", lf)
	gotCRLF := ChunkMarkdown("f.md", crlf)

	// Assert — chunk the same, no stray \r anywhere
	if len(gotLF) != len(gotCRLF) {
		t.Fatalf("LF gave %d chunks, CRLF gave %d", len(gotLF), len(gotCRLF))
	}
	for i := range gotLF {
		if gotLF[i].Heading != gotCRLF[i].Heading {
			t.Errorf("chunk %d heading differs: %q vs %q", i, gotLF[i].Heading, gotCRLF[i].Heading)
		}
		if gotLF[i].Text != gotCRLF[i].Text {
			t.Errorf("chunk %d text differs: %q vs %q", i, gotLF[i].Text, gotCRLF[i].Text)
		}
		if gotLF[i].Hash != gotCRLF[i].Hash {
			t.Errorf("chunk %d hash differs", i)
		}
		if strings.ContainsRune(gotCRLF[i].Text, '\r') {
			t.Errorf("chunk %d CRLF text contains stray \\r: %q", i, gotCRLF[i].Text)
		}
		if strings.ContainsRune(gotCRLF[i].Heading, '\r') {
			t.Errorf("chunk %d CRLF heading contains stray \\r: %q", i, gotCRLF[i].Heading)
		}
	}
}
