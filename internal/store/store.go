// Package store persists a code index in a single SQLite database: file
// metadata for incremental re-indexing, chunk text and embeddings for search,
// and an FTS5 index for BM25 lexical ranking. It is pure Go (no CGO) via the
// modernc.org/sqlite driver, which ships FTS5 built in.
package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/vaughanb/agent-context-go/internal/core"

	_ "modernc.org/sqlite"
)

const schemaVersion = "1"

// ErrModelMismatch is returned by New when an existing database was built with
// a different embedding model or dimensionality than requested. The caller
// must Clear and re-index (embeddings from different models are not
// comparable).
var ErrModelMismatch = errors.New("store: embedding model or dimension mismatch")

// Store is a handle to one codebase's index database. It is safe for
// concurrent use by multiple goroutines (database/sql pools connections).
type Store struct {
	db             *sql.DB
	model          string
	dim            int
	chunkerVersion string

	// embMu guards the in-memory embedding cache. The cache holds every
	// chunk's id and vector so repeated searches avoid re-scanning and
	// re-decoding the whole chunks table; any write invalidates it.
	embMu    sync.RWMutex
	embRows  []core.EmbRow
	embValid bool
}

// New opens (creating if needed) the index database at path, applies the
// schema, and reconciles the metadata. If the database already records a
// different embedding model or dimension, it returns ErrModelMismatch. If it
// records a different chunkerVersion, every file is marked changed so the
// next index run re-chunks it — existing chunks stay searchable until then.
func New(ctx context.Context, path, model string, dim int, chunkerVersion string) (*Store, error) {
	if model == "" {
		return nil, fmt.Errorf("store: model must not be empty")
	}
	if dim <= 0 {
		return nil, fmt.Errorf("store: dim must be positive, got %d", dim)
	}
	if chunkerVersion == "" {
		return nil, fmt.Errorf("store: chunker version must not be empty")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create index dir: %w", err)
		}
	}

	// Foreign keys drive ON DELETE CASCADE; busy_timeout avoids spurious
	// "database is locked" errors under concurrent access.
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}

	s := &Store{db: db, model: model, dim: dim, chunkerVersion: chunkerVersion}
	if err := s.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return s.reconcileMeta(ctx)
}

func (s *Store) reconcileMeta(ctx context.Context) error {
	existing, err := s.readMeta(ctx)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return s.writeMeta(ctx, map[string]string{
			"schema_version":  schemaVersion,
			"model":           s.model,
			"dim":             strconv.Itoa(s.dim),
			"chunker_version": s.chunkerVersion,
		})
	}
	if existing["model"] != s.model || existing["dim"] != strconv.Itoa(s.dim) {
		return fmt.Errorf("%w: existing model=%q dim=%s, requested model=%q dim=%d",
			ErrModelMismatch, existing["model"], existing["dim"], s.model, s.dim)
	}
	if existing["chunker_version"] != s.chunkerVersion {
		// The chunking algorithm changed (databases predating the key count as
		// changed too). Stored chunks remain searchable, but their boundaries
		// are stale; clearing every content hash marks all files changed so the
		// next index run re-chunks and re-embeds them.
		if _, err := s.db.ExecContext(ctx, `UPDATE files SET content_hash = ''`); err != nil {
			return fmt.Errorf("mark files for re-chunk: %w", err)
		}
		return s.writeMeta(ctx, map[string]string{"chunker_version": s.chunkerVersion})
	}
	return nil
}

func (s *Store) readMeta(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM meta`)
	if err != nil {
		return nil, fmt.Errorf("read meta: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan meta: %w", err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate meta: %w", err)
	}
	return out, nil
}

func (s *Store) writeMeta(ctx context.Context, kv map[string]string) error {
	for k, v := range kv {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO meta(key, value) VALUES(?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return fmt.Errorf("write meta %q: %w", k, err)
		}
	}
	return nil
}

// UpsertFile ensures a metadata row exists for f (keyed by path) and reports
// whether the file is new or its content differs from the last successfully
// indexed version. When changed is true the caller should re-chunk and call
// ReplaceChunks, which is where the new content hash is committed; when false
// the stored chunks are still current. Crucially, UpsertFile does not advance
// the stored hash — so if indexing fails after this call, the file is treated
// as changed again on the next run rather than being silently skipped. The
// returned id is the file's row id.
func (s *Store) UpsertFile(ctx context.Context, f core.File) (id int64, changed bool, err error) {
	var existingID int64
	var existingHash string
	row := s.db.QueryRowContext(ctx, `SELECT id, content_hash FROM files WHERE path = ?`, f.Path)
	switch err := row.Scan(&existingID, &existingHash); {
	case errors.Is(err, sql.ErrNoRows):
		// Insert with an empty hash placeholder: the real hash is committed by
		// ReplaceChunks only once chunks are stored.
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO files(path, mod_time, size, content_hash, chunk_count)
			 VALUES(?, ?, ?, '', 0)`, f.Path, f.ModTimeUnix, f.Size)
		if err != nil {
			return 0, false, fmt.Errorf("insert file %q: %w", f.Path, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return 0, false, fmt.Errorf("last insert id for %q: %w", f.Path, err)
		}
		return id, true, nil
	case err != nil:
		return 0, false, fmt.Errorf("lookup file %q: %w", f.Path, err)
	}

	return existingID, existingHash != f.ContentHash, nil
}

// ReplaceChunks atomically replaces all chunks for fileID with the given chunks
// and their embeddings, and commits meta's content hash, size, and mod time as
// the file's now-current state. Committing the hash here (rather than in
// UpsertFile) means the file is marked up to date only after its chunks are
// durably stored. vecs must be parallel to chunks and every vector must have
// the store's dimensionality. Passing no chunks clears the file's chunks while
// still committing the hash (e.g. a file that legitimately yields nothing).
func (s *Store) ReplaceChunks(ctx context.Context, fileID int64, meta core.File, chunks []core.Chunk, vecs [][]float32) (err error) {
	if len(chunks) != len(vecs) {
		return fmt.Errorf("store: chunks (%d) and vectors (%d) length mismatch", len(chunks), len(vecs))
	}
	for i, v := range vecs {
		if len(v) != s.dim {
			return fmt.Errorf("store: vector %d has dim %d, want %d", i, len(v), s.dim)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, `DELETE FROM chunks WHERE file_id = ?`, fileID); err != nil {
		return fmt.Errorf("delete existing chunks for file %d: %w", fileID, err)
	}
	for i, c := range chunks {
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO chunks(file_id, start_line, end_line, symbol, content, embedding)
			 VALUES(?, ?, ?, ?, ?, ?)`,
			fileID, c.StartLine, c.EndLine, c.Symbol, c.Content, encodeVec(vecs[i])); err != nil {
			return fmt.Errorf("insert chunk %d for file %d: %w", i, fileID, err)
		}
	}
	if _, err = tx.ExecContext(ctx,
		`UPDATE files SET mod_time = ?, size = ?, content_hash = ?, chunk_count = ? WHERE id = ?`,
		meta.ModTimeUnix, meta.Size, meta.ContentHash, len(chunks), fileID); err != nil {
		return fmt.Errorf("commit file meta for %d: %w", fileID, err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit replace chunks: %w", err)
	}
	s.invalidateEmbeddings()
	return nil
}

// DeleteFile removes a file and all of its chunks (via ON DELETE CASCADE). It
// is a no-op if the path is not indexed.
func (s *Store) DeleteFile(ctx context.Context, path string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM files WHERE path = ?`, path); err != nil {
		return fmt.Errorf("delete file %q: %w", path, err)
	}
	s.invalidateEmbeddings()
	return nil
}

// ListPaths returns every indexed file path. The indexer uses it to detect
// files deleted from disk since the last run.
func (s *Store) ListPaths(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM files`)
	if err != nil {
		return nil, fmt.Errorf("list paths: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan path: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate paths: %w", err)
	}
	return out, nil
}

// AllEmbeddings returns every chunk id with its decoded embedding, for
// brute-force dense (cosine) scoring. The result is cached in memory and
// reused until a write (ReplaceChunks, DeleteFile, Clear) invalidates it, so
// steady-state searches avoid re-scanning and re-decoding every embedding
// BLOB. Callers must treat the returned slice and its vectors as read-only.
func (s *Store) AllEmbeddings(ctx context.Context) ([]core.EmbRow, error) {
	s.embMu.RLock()
	if s.embValid {
		rows := s.embRows
		s.embMu.RUnlock()
		return rows, nil
	}
	s.embMu.RUnlock()

	s.embMu.Lock()
	defer s.embMu.Unlock()
	if s.embValid {
		return s.embRows, nil
	}
	rows, err := s.loadEmbeddings(ctx)
	if err != nil {
		return nil, err
	}
	s.embRows, s.embValid = rows, true
	return rows, nil
}

func (s *Store) loadEmbeddings(ctx context.Context) ([]core.EmbRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, embedding FROM chunks WHERE embedding IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("query embeddings: %w", err)
	}
	defer rows.Close()

	var out []core.EmbRow
	for rows.Next() {
		var r core.EmbRow
		var blob []byte
		if err := rows.Scan(&r.ChunkID, &blob); err != nil {
			return nil, fmt.Errorf("scan embedding row: %w", err)
		}
		vec, err := decodeVec(blob)
		if err != nil {
			return nil, fmt.Errorf("decode vector for chunk %d: %w", r.ChunkID, err)
		}
		r.Vector = vec
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate embeddings: %w", err)
	}
	return out, nil
}

// invalidateEmbeddings drops the embedding cache so the next AllEmbeddings
// reloads from the database. Called after every write that touches chunks.
func (s *Store) invalidateEmbeddings() {
	s.embMu.Lock()
	s.embRows, s.embValid = nil, false
	s.embMu.Unlock()
}

// ChunksByIDs returns the full stored rows for the given chunk ids, keyed by
// id; ids not present in the index are simply absent from the result. Dense
// search scores against the lean embedding cache and hydrates only its top
// candidates through this method.
func (s *Store) ChunksByIDs(ctx context.Context, ids []int64) (map[int64]core.Hit, error) {
	out := make(map[int64]core.Hit, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, f.path, c.start_line, c.end_line, c.symbol, c.content
		 FROM chunks c JOIN files f ON f.id = c.file_id
		 WHERE c.id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("query chunks by id: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var h core.Hit
		if err := rows.Scan(&h.ChunkID, &h.Path, &h.StartLine, &h.EndLine, &h.Symbol, &h.Content); err != nil {
			return nil, fmt.Errorf("scan chunk row: %w", err)
		}
		out[h.ChunkID] = h
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunk rows: %w", err)
	}
	return out, nil
}

// LexicalSearch runs a BM25 full-text query and returns up to k hits ordered
// best-first. Scores are normalized so that larger is better (SQLite's bm25()
// returns smaller-is-better). Returns no hits for an empty query.
func (s *Store) LexicalSearch(ctx context.Context, query string, k int) ([]core.Hit, error) {
	match := buildFTSQuery(query)
	if match == "" || k <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, f.path, c.start_line, c.end_line, c.symbol, c.content, bm25(chunks_fts) AS score
		 FROM chunks_fts
		 JOIN chunks c ON c.id = chunks_fts.rowid
		 JOIN files f ON f.id = c.file_id
		 WHERE chunks_fts MATCH ?
		 ORDER BY score
		 LIMIT ?`, match, k)
	if err != nil {
		return nil, fmt.Errorf("lexical search: %w", err)
	}
	defer rows.Close()

	var out []core.Hit
	for rows.Next() {
		var h core.Hit
		var bm25 float64
		if err := rows.Scan(&h.ChunkID, &h.Path, &h.StartLine, &h.EndLine, &h.Symbol, &h.Content, &bm25); err != nil {
			return nil, fmt.Errorf("scan lexical hit: %w", err)
		}
		h.Score = -bm25 // flip so larger is better, matching dense scores
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate lexical hits: %w", err)
	}
	return out, nil
}

// Clear removes all files and chunks (and thus all FTS entries) while keeping
// the meta row, so the index can be rebuilt with the same model.
func (s *Store) Clear(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM files`); err != nil {
		return fmt.Errorf("clear files: %w", err)
	}
	// files → chunks cascade fires the FTS delete triggers; rebuild guards
	// against any drift in the external-content FTS index.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("rebuild fts: %w", err)
	}
	s.invalidateEmbeddings()
	return nil
}

// Dim reports the embedding dimensionality this store was opened with.
func (s *Store) Dim() int { return s.dim }

// Model reports the embedding model this store was opened with.
func (s *Store) Model() string { return s.model }

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// buildFTSQuery turns arbitrary user text into a safe FTS5 MATCH expression:
// each alphanumeric token is quoted (so punctuation and FTS operators cannot
// break the query or be interpreted), marked as a prefix (code identifiers
// are compound — "damage" should match "damageAmount"), and tokens are OR-ed
// together.
func buildFTSQuery(query string) string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return !(r == '_' ||
			(r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9'))
	})
	quoted := make([]string, 0, len(fields))
	for _, f := range fields {
		quoted = append(quoted, `"`+f+`"*`)
	}
	return strings.Join(quoted, " OR ")
}

// encodeVec serializes a float32 slice as little-endian bytes for BLOB storage.
func encodeVec(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// decodeVec reverses encodeVec.
func decodeVec(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("embedding blob length %d is not a multiple of 4", len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out, nil
}
