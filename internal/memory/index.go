package memory

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO) — enables cross-compilation
)

const (
	embedFormatVersion = 1 // bump if the BLOB encoding changes
	chunkerVersion     = 1 // bump if ChunkMarkdown's output changes
)

// IndexMeta identifies the embedding space the stored vectors belong to. Any
// mismatch on open invalidates the cache (vectors become incomparable).
type IndexMeta struct {
	Provider           string
	Model              string
	Dim                int
	EmbedFormatVersion int
	ChunkerVersion     int
}

// IndexedChunk is a Chunk plus the scope key and embedding vector it was stored
// under.
type IndexedChunk struct {
	Chunk           // embedded source-chunk fields
	ScopeKey string // scope/physical-path key (NOT bare filename)
	Vector   []float32
}

// Index is a pure-Go SQLite sidecar that persists embedding vectors so semantic
// recall survives restarts and only re-embeds changed chunks.
type Index struct {
	db   *sql.DB
	meta IndexMeta
}

// schema creates the meta and chunks tables if they do not already exist.
const schema = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT
);
CREATE TABLE IF NOT EXISTS chunks (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	scope_key  TEXT NOT NULL,
	file       TEXT NOT NULL,
	heading    TEXT,
	text       TEXT NOT NULL,
	line_start INTEGER,
	line_end   INTEGER,
	hash       TEXT NOT NULL,
	vector     BLOB NOT NULL,
	UNIQUE(scope_key, hash)
);
`

// OpenIndex opens/creates the DB at path, ensures the schema, and validates
// meta. If stored meta differs from want in any field it WIPES the chunks table
// (re-embed needed) and writes want as the new meta.
func OpenIndex(path string, want IndexMeta) (*Index, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open index db: %w", err)
	}
	applyConcurrencyPragmas(db)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create index schema: %w", err)
	}

	if err := reconcileMeta(db, want); err != nil {
		db.Close()
		return nil, err
	}
	return &Index{db: db, meta: want}, nil
}

// OpenIndexReadOnly opens an EXISTING index DB and reads its stored meta WITHOUT
// reconciling/wiping. It is used for status reporting: it must never mutate or
// invalidate the index. The schema is ensured (so Count works on a tables-present
// DB) but no chunks are deleted regardless of meta.
func OpenIndexReadOnly(path string) (*Index, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open index db: %w", err)
	}
	applyConcurrencyPragmas(db)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("ensure index schema: %w", err)
	}
	meta, _, err := readMeta(db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("read index meta: %w", err)
	}
	return &Index{db: db, meta: meta}, nil
}

// applyConcurrencyPragmas switches the connection to WAL journaling and a 5s busy
// timeout so multiple mcp-server processes sharing one embeddings.db can interleave
// index writes/reads without instant "database is locked" failures. Best-effort:
// a PRAGMA error is non-fatal (the index simply falls back to default behavior).
func applyConcurrencyPragmas(db *sql.DB) {
	_, _ = db.Exec("PRAGMA journal_mode=WAL;")
	_, _ = db.Exec("PRAGMA busy_timeout=5000;")
}

// Count returns the number of stored chunk vectors.
func (ix *Index) Count() (int, error) {
	var n int
	if err := ix.db.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&n); err != nil {
		return 0, fmt.Errorf("count chunks: %w", err)
	}
	return n, nil
}

// reconcileMeta compares stored meta with want: a fresh DB just gets want
// written; an exact match is left alone; any field difference wipes chunks and
// overwrites meta.
func reconcileMeta(db *sql.DB, want IndexMeta) error {
	stored, found, err := readMeta(db)
	if err != nil {
		return err
	}
	if found && stored == want {
		return nil
	}
	if found {
		if _, err := db.Exec("DELETE FROM chunks"); err != nil {
			return fmt.Errorf("invalidate chunks on meta mismatch: %w", err)
		}
	}
	return writeMeta(db, want)
}

// metaKeys maps each meta row key to a getter/setter against an IndexMeta. The
// reads parse strings back into the typed fields.
var metaKeys = []string{"provider", "model", "dim", "embed_format_version", "chunker_version"}

// readMeta loads the five meta rows. found is false when the table has none of
// them (a fresh DB).
func readMeta(db *sql.DB) (IndexMeta, bool, error) {
	rows, err := db.Query("SELECT key, value FROM meta")
	if err != nil {
		return IndexMeta{}, false, fmt.Errorf("read meta: %w", err)
	}
	defer rows.Close()

	values := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return IndexMeta{}, false, fmt.Errorf("scan meta row: %w", err)
		}
		values[k] = v
	}
	if err := rows.Err(); err != nil {
		return IndexMeta{}, false, fmt.Errorf("iterate meta rows: %w", err)
	}
	if len(values) == 0 {
		return IndexMeta{}, false, nil
	}
	return parseMeta(values)
}

// parseMeta converts the raw key/value map into a typed IndexMeta.
func parseMeta(values map[string]string) (IndexMeta, bool, error) {
	dim, err := strconv.Atoi(values["dim"])
	if err != nil {
		return IndexMeta{}, false, fmt.Errorf("parse meta dim %q: %w", values["dim"], err)
	}
	efv, err := strconv.Atoi(values["embed_format_version"])
	if err != nil {
		return IndexMeta{}, false, fmt.Errorf("parse embed_format_version %q: %w", values["embed_format_version"], err)
	}
	cv, err := strconv.Atoi(values["chunker_version"])
	if err != nil {
		return IndexMeta{}, false, fmt.Errorf("parse chunker_version %q: %w", values["chunker_version"], err)
	}
	return IndexMeta{
		Provider:           values["provider"],
		Model:              values["model"],
		Dim:                dim,
		EmbedFormatVersion: efv,
		ChunkerVersion:     cv,
	}, true, nil
}

// writeMeta upserts all five meta rows to reflect m.
func writeMeta(db *sql.DB, m IndexMeta) error {
	rows := map[string]string{
		"provider":             m.Provider,
		"model":                m.Model,
		"dim":                  strconv.Itoa(m.Dim),
		"embed_format_version": strconv.Itoa(m.EmbedFormatVersion),
		"chunker_version":      strconv.Itoa(m.ChunkerVersion),
	}
	for _, key := range metaKeys {
		_, err := db.Exec(
			"INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
			key, rows[key],
		)
		if err != nil {
			return fmt.Errorf("write meta %s: %w", key, err)
		}
	}
	return nil
}

// Close releases the underlying database handle.
func (ix *Index) Close() error {
	if err := ix.db.Close(); err != nil {
		return fmt.Errorf("close index db: %w", err)
	}
	return nil
}

// Meta returns the embedding-space metadata the index is bound to.
func (ix *Index) Meta() IndexMeta { return ix.meta }

// Put upserts one chunk's vector, keyed by (scopeKey, Chunk.Hash). An existing
// pair is a no-op so unchanged chunks are never re-stored. It errors if the
// vector length does not match Meta().Dim.
func (ix *Index) Put(scopeKey string, c Chunk, vector []float32) error {
	if len(vector) != ix.meta.Dim {
		return fmt.Errorf("vector dim %d != index dim %d", len(vector), ix.meta.Dim)
	}
	blob := encodeVector(vector)
	_, err := ix.db.Exec(`
		INSERT INTO chunks(scope_key, file, heading, text, line_start, line_end, hash, vector)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope_key, hash) DO NOTHING`,
		scopeKey, c.File, c.Heading, c.Text, c.LineStart, c.LineEnd, c.Hash, blob,
	)
	if err != nil {
		return fmt.Errorf("put chunk (%s, %s): %w", scopeKey, c.Hash, err)
	}
	return nil
}

// Has reports whether (scopeKey, hash) is already stored.
func (ix *Index) Has(scopeKey, hash string) (bool, error) {
	var one int
	err := ix.db.QueryRow(
		"SELECT 1 FROM chunks WHERE scope_key = ? AND hash = ? LIMIT 1",
		scopeKey, hash,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("has chunk (%s, %s): %w", scopeKey, hash, err)
	}
	return true, nil
}

// Load returns all indexed chunks for which allow(scopeKey, file) is true. A nil
// allow loads everything. Rows whose stored vector fails to decode or whose
// length disagrees with Meta().Dim are skipped (defends against corruption).
func (ix *Index) Load(allow func(scopeKey, file string) bool) ([]IndexedChunk, error) {
	rows, err := ix.db.Query(
		"SELECT scope_key, file, heading, text, line_start, line_end, hash, vector FROM chunks",
	)
	if err != nil {
		return nil, fmt.Errorf("load chunks: %w", err)
	}
	defer rows.Close()

	var out []IndexedChunk
	for rows.Next() {
		ic, blob, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		if allow != nil && !allow(ic.ScopeKey, ic.File) {
			continue
		}
		vec, err := decodeVector(blob)
		if err != nil || len(vec) != ix.meta.Dim {
			continue // skip corrupt/garbage row
		}
		ic.Vector = vec
		out = append(out, ic)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunks: %w", err)
	}
	return out, nil
}

// scanChunk reads one chunk row into an IndexedChunk (sans decoded vector) plus
// the raw vector blob.
func scanChunk(rows *sql.Rows) (IndexedChunk, []byte, error) {
	var ic IndexedChunk
	var blob []byte
	err := rows.Scan(
		&ic.ScopeKey, &ic.File, &ic.Heading, &ic.Text,
		&ic.LineStart, &ic.LineEnd, &ic.Hash, &blob,
	)
	if err != nil {
		return IndexedChunk{}, nil, fmt.Errorf("scan chunk row: %w", err)
	}
	return ic, blob, nil
}

// PruneExcept deletes stored chunks for scopeKey whose hash is not in keep
// (removes chunks deleted from the source file).
func (ix *Index) PruneExcept(scopeKey string, keep map[string]bool) error {
	rows, err := ix.db.Query("SELECT hash FROM chunks WHERE scope_key = ?", scopeKey)
	if err != nil {
		return fmt.Errorf("scan scope %s for prune: %w", scopeKey, err)
	}
	var stale []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return fmt.Errorf("scan prune hash: %w", err)
		}
		if !keep[h] {
			stale = append(stale, h)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate prune hashes: %w", err)
	}
	rows.Close()

	for _, h := range stale {
		if _, err := ix.db.Exec("DELETE FROM chunks WHERE scope_key = ? AND hash = ?", scopeKey, h); err != nil {
			return fmt.Errorf("prune chunk (%s, %s): %w", scopeKey, h, err)
		}
	}
	return nil
}

// pruneBatchSize keeps each delete under SQLite's default 999-parameter limit
// (one bound parameter per stale scope_key).
const pruneBatchSize = 900

// PruneScopesExcept deletes every chunk whose scope_key is not in keep.
// Sweeps chunks orphaned by file deletion or workspace-over-global shadowing.
//
// An empty keep set clears the table outright. Otherwise it first reads the
// DISTINCT scope_keys currently stored, computes the set absent from keep, and
// deletes those stale scopes via bound parameters (never string-concatenated),
// chunked to stay under the SQLite parameter limit. Deleting the stale set —
// rather than a per-batch NOT IN over kept keys — keeps the result correct when
// keep spans multiple batches.
func (ix *Index) PruneScopesExcept(keep map[string]bool) error {
	if len(keep) == 0 {
		if _, err := ix.db.Exec("DELETE FROM chunks"); err != nil {
			return fmt.Errorf("prune all chunks: %w", err)
		}
		return nil
	}

	stale, err := ix.staleScopes(keep)
	if err != nil {
		return err
	}
	for start := 0; start < len(stale); start += pruneBatchSize {
		end := start + pruneBatchSize
		if end > len(stale) {
			end = len(stale)
		}
		if err := ix.deleteScopesIn(stale[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// staleScopes returns the DISTINCT stored scope_keys that are absent from keep.
func (ix *Index) staleScopes(keep map[string]bool) ([]string, error) {
	rows, err := ix.db.Query("SELECT DISTINCT scope_key FROM chunks")
	if err != nil {
		return nil, fmt.Errorf("scan scopes for prune: %w", err)
	}
	defer rows.Close()

	var stale []string
	for rows.Next() {
		var sk string
		if err := rows.Scan(&sk); err != nil {
			return nil, fmt.Errorf("scan prune scope: %w", err)
		}
		if !keep[sk] {
			stale = append(stale, sk)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate prune scopes: %w", err)
	}
	return stale, nil
}

// deleteScopesIn runs a single DELETE ... WHERE scope_key IN (?,?,...) for one
// batch of stale keys, passing the keys as bound parameters.
func (ix *Index) deleteScopesIn(keys []string) error {
	placeholders := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, k := range keys {
		placeholders[i] = "?"
		args[i] = k
	}
	query := "DELETE FROM chunks WHERE scope_key IN (" + strings.Join(placeholders, ",") + ")"
	if _, err := ix.db.Exec(query, args...); err != nil {
		return fmt.Errorf("prune stale scopes batch: %w", err)
	}
	return nil
}
