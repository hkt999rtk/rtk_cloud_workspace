package rag

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var indexableSuffixes = map[string]bool{
	".md": true, ".markdown": true, ".yaml": true, ".yml": true, ".toml": true, ".json": true,
	".go": true, ".js": true, ".jsx": true, ".mjs": true, ".ts": true, ".tsx": true,
}

var excludedParts = map[string]bool{
	".git": true, ".rag": true, ".claude": true, ".codex": true, ".worktrees": true,
	"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true,
	".cache": true, "__pycache__": true, "coverage": true, "fixtures": true, "3rd_party": true,
}

var codeIncludeHints = []string{
	"/cmd/",
	"/internal/config/",
	"/internal/app/",
	"/internal/routes",
	"/web/src/routes",
	"/web/src/http",
	"main.go",
}

type Index struct {
	Workspace string
	DBPath    string
	AI        AIClient
	repos     []RepositoryInfo
}

func NewIndex(workspace, dbPath string, ai AIClient) *Index {
	workspace = mustAbs(workspace)
	if dbPath == "" {
		dbPath = filepath.Join(workspace, ".rag", "rag.db")
	}
	if ai == nil {
		ai = NewOpenAIClient()
	}
	return &Index{Workspace: workspace, DBPath: mustAbs(dbPath), AI: ai}
}

func (idx *Index) Connect() (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(idx.DBPath), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", idx.DBPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("pragma journal_mode = wal"); err != nil {
		db.Close()
		return nil, err
	}
	if err := idx.EnsureSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (idx *Index) EnsureSchema(db *sql.DB) error {
	if _, err := db.Exec(`
create table if not exists repositories (
  path text primary key,
  name text not null,
  commit_sha text not null,
  branch_or_detached_state text not null,
  dirty integer not null,
  indexed_at real not null
);
create table if not exists documents (
  path text primary key,
  repo_name text not null,
  submodule_path text not null,
  commit_sha text not null,
  branch_or_detached_state text not null,
  doc_classification text not null,
  source_layer text not null,
  content_hash text not null,
  active integer not null,
  indexed_at real not null
);
create table if not exists chunks (
  id integer primary key autoincrement,
  document_path text not null,
  repo_name text not null,
  submodule_path text not null,
  commit_sha text not null,
  branch_or_detached_state text not null,
  heading text not null,
  line_start integer not null,
  line_end integer not null,
  doc_classification text not null,
  source_layer text not null,
  content text not null,
  content_hash text not null,
  embedding_json text,
  active integer not null,
  indexed_at real not null
);`); err != nil {
		return err
	}
	if tableExists(db, "chunk_fts") {
		return nil
	}
	if _, err := db.Exec(`
create virtual table if not exists chunk_fts using fts5(
  content,
  heading,
  document_path unindexed,
  content='chunks',
  content_rowid='id'
);`); err != nil {
		_, fallbackErr := db.Exec(`
create table if not exists chunk_fts (
  rowid integer primary key,
  content text not null,
  heading text not null,
  document_path text not null
);`)
		return fallbackErr
	}
	return nil
}

func tableExists(db *sql.DB, name string) bool {
	var found string
	err := db.QueryRow("select name from sqlite_master where name = ? limit 1", name).Scan(&found)
	return err == nil && found == name
}

func (idx *Index) IndexFull(ctx context.Context) (IndexResult, error) {
	db, err := idx.Connect()
	if err != nil {
		return IndexResult{}, err
	}
	defer db.Close()
	idx.repos = DiscoverRepositories(idx.Workspace)
	defer func() { idx.repos = nil }()
	if err := idx.updateRepositories(db); err != nil {
		return IndexResult{}, err
	}
	paths, err := idx.IterIndexableFiles()
	if err != nil {
		return IndexResult{}, err
	}
	active := map[string]bool{}
	for _, path := range paths {
		rel, err := relSlash(idx.Workspace, path)
		if err == nil {
			active[rel] = true
		}
	}
	rows, err := db.Query("select path from documents where active = 1")
	if err != nil {
		return IndexResult{}, err
	}
	var existing []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err == nil {
			existing = append(existing, path)
		}
	}
	rows.Close()
	for _, path := range existing {
		if !active[path] {
			if err := idx.deactivateDocument(db, path); err != nil {
				return IndexResult{}, err
			}
		}
	}
	result := IndexResult{ActiveFiles: len(active)}
	for _, path := range paths {
		changed, err := idx.indexFileIfChanged(ctx, db, path)
		if err != nil {
			return result, err
		}
		if changed {
			result.Indexed++
		} else {
			result.Skipped++
		}
	}
	return result, nil
}

func (idx *Index) IndexChanged(ctx context.Context) (IndexResult, error) {
	return idx.IndexFull(ctx)
}

func (idx *Index) updateRepositories(db *sql.DB) error {
	now := float64(time.Now().UnixNano()) / 1e9
	for _, repo := range idx.repos {
		dirty := 0
		if repo.Dirty {
			dirty = 1
		}
		_, err := db.Exec(`
insert into repositories(path, name, commit_sha, branch_or_detached_state, dirty, indexed_at)
values (?, ?, ?, ?, ?, ?)
on conflict(path) do update set
  name=excluded.name,
  commit_sha=excluded.commit_sha,
  branch_or_detached_state=excluded.branch_or_detached_state,
  dirty=excluded.dirty,
  indexed_at=excluded.indexed_at`,
			repo.Path, repo.Name, repo.CommitSHA, repo.BranchOrDetachedState, dirty, now)
		if err != nil {
			return err
		}
	}
	return nil
}

func (idx *Index) IterIndexableFiles() ([]string, error) {
	var files []string
	err := filepath.WalkDir(idx.Workspace, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != idx.Workspace && (excludedParts[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			rel, relErr := relSlash(idx.Workspace, path)
			if relErr == nil {
				for _, part := range strings.Split(rel, "/") {
					if excludedParts[part] {
						return filepath.SkipDir
					}
				}
			}
			return nil
		}
		if idx.ShouldIndex(path) {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func (idx *Index) ShouldIndex(path string) bool {
	rel, err := relSlash(idx.Workspace, path)
	if err != nil {
		return false
	}
	suffix := strings.ToLower(filepath.Ext(path))
	if !indexableSuffixes[suffix] {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if excludedParts[part] {
			return false
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > 512000 {
		return false
	}
	if hasAnySuffix(rel, ".go", ".js", ".jsx", ".mjs", ".ts", ".tsx") {
		withSlash := "/" + rel
		for _, hint := range codeIncludeHints {
			if strings.Contains(withSlash, hint) {
				return true
			}
		}
		return false
	}
	return true
}

func (idx *Index) indexFileIfChanged(ctx context.Context, db *sql.DB, path string) (bool, error) {
	rel, err := relSlash(idx.Workspace, path)
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	text := string(raw)
	contentHash := sha256Hex(text)
	var currentHash string
	var active int
	err = db.QueryRow("select content_hash, active from documents where path = ?", rel).Scan(&currentHash, &active)
	if err == nil && currentHash == contentHash && active == 1 {
		needs, err := idx.needsEmbeddingRefresh(db, rel)
		if err != nil {
			return false, err
		}
		if !needs {
			return false, nil
		}
	} else if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err := idx.deactivateDocument(db, rel); err != nil {
		return false, err
	}
	repo := repoForPath(idx.Workspace, path, idx.repos)
	classification, layer := ClassifyDocument(rel)
	now := float64(time.Now().UnixNano()) / 1e9
	_, err = db.Exec(`
insert into documents(path, repo_name, submodule_path, commit_sha, branch_or_detached_state,
  doc_classification, source_layer, content_hash, active, indexed_at)
values (?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
on conflict(path) do update set
  repo_name=excluded.repo_name,
  submodule_path=excluded.submodule_path,
  commit_sha=excluded.commit_sha,
  branch_or_detached_state=excluded.branch_or_detached_state,
  doc_classification=excluded.doc_classification,
  source_layer=excluded.source_layer,
  content_hash=excluded.content_hash,
  active=1,
  indexed_at=excluded.indexed_at`,
		rel, repo.Name, repo.Path, repo.CommitSHA, repo.BranchOrDetachedState, classification, layer, contentHash, now)
	if err != nil {
		return false, err
	}
	chunks, err := idx.ChunkFile(path)
	if err != nil {
		return false, err
	}
	contents := make([]string, len(chunks))
	for i, chunk := range chunks {
		contents[i] = short(chunk.Content, 4000)
	}
	embeddings := idx.AI.EmbedMany(ctx, contents)
	for i, chunk := range chunks {
		var embeddingJSON sql.NullString
		if i < len(embeddings) && embeddings[i] != nil {
			raw, _ := json.Marshal(embeddings[i])
			embeddingJSON = sql.NullString{String: string(raw), Valid: true}
		}
		cursor, err := db.Exec(`
insert into chunks(document_path, repo_name, submodule_path, commit_sha, branch_or_detached_state,
  heading, line_start, line_end, doc_classification, source_layer, content, content_hash,
  embedding_json, active, indexed_at)
values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
			chunk.FilePath, chunk.RepoName, chunk.SubmodulePath, chunk.CommitSHA, chunk.BranchOrDetachedState,
			chunk.Heading, chunk.LineStart, chunk.LineEnd, chunk.DocClassification, chunk.SourceLayer,
			chunk.Content, chunk.ContentHash, embeddingJSON, now)
		if err != nil {
			return false, err
		}
		id, err := cursor.LastInsertId()
		if err != nil {
			return false, err
		}
		_, _ = db.Exec("insert into chunk_fts(rowid, content, heading, document_path) values (?, ?, ?, ?)", id, chunk.Content, chunk.Heading, chunk.FilePath)
	}
	return true, nil
}

func (idx *Index) needsEmbeddingRefresh(db *sql.DB, rel string) (bool, error) {
	if !idx.AI.EmbeddingsEnabled() {
		return false, nil
	}
	var missing int
	err := db.QueryRow("select count(*) from chunks where document_path = ? and active = 1 and embedding_json is null", rel).Scan(&missing)
	return missing > 0, err
}

func (idx *Index) deactivateDocument(db *sql.DB, rel string) error {
	rows, err := db.Query("select id from chunks where document_path = ? and active = 1", rel)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		_, _ = db.Exec("delete from chunk_fts where rowid = ?", id)
	}
	if _, err := db.Exec("update chunks set active = 0 where document_path = ?", rel); err != nil {
		return err
	}
	_, err = db.Exec("update documents set active = 0 where path = ?", rel)
	return err
}

func (idx *Index) ChunkFile(path string) ([]Chunk, error) {
	path = mustAbs(path)
	rel, err := relSlash(idx.Workspace, path)
	if err != nil {
		return nil, err
	}
	repo := repoForPath(idx.Workspace, path, idx.repos)
	classification, layer := ClassifyDocument(rel)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var spans []span
	if hasAnySuffix(rel, ".md", ".markdown") {
		spans = markdownSpans(lines)
	} else {
		spans = plainSpans(lines)
	}
	chunks := make([]Chunk, 0, len(spans))
	for _, sp := range spans {
		normalized := strings.TrimSpace(sp.Content)
		if normalized == "" {
			continue
		}
		heading := sp.Heading
		if heading == "" {
			heading = filepath.Base(rel)
		}
		chunks = append(chunks, Chunk{
			Content:               normalized,
			Heading:               heading,
			WorkspacePath:         idx.Workspace,
			RepoName:              repo.Name,
			SubmodulePath:         repo.Path,
			CommitSHA:             repo.CommitSHA,
			BranchOrDetachedState: repo.BranchOrDetachedState,
			FilePath:              rel,
			LineStart:             sp.Start,
			LineEnd:               sp.End,
			DocClassification:     classification,
			SourceLayer:           layer,
			ContentHash:           sha256Hex(normalized),
		})
	}
	return chunks, nil
}

type span struct {
	Start   int
	End     int
	Heading string
	Content string
}

var markdownHeadingRE = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)

func markdownSpans(lines []string) []span {
	var spans []span
	var stack []struct {
		level int
		text  string
	}
	start := 1
	activeHeading := ""
	var buffer []string
	flush := func(endLine int) {
		if strings.TrimSpace(strings.Join(buffer, "\n")) != "" {
			spans = append(spans, span{Start: start, End: endLine, Heading: activeHeading, Content: strings.Join(buffer, "\n")})
		}
	}
	for i, line := range lines {
		lineNo := i + 1
		match := markdownHeadingRE.FindStringSubmatch(line)
		if match != nil {
			flush(lineNo - 1)
			level := len(match[1])
			title := strings.TrimSpace(match[2])
			next := stack[:0]
			for _, item := range stack {
				if item.level < level {
					next = append(next, item)
				}
			}
			stack = append(next, struct {
				level int
				text  string
			}{level: level, text: title})
			var headings []string
			for _, item := range stack {
				headings = append(headings, item.text)
			}
			activeHeading = strings.Join(headings, " > ")
			start = lineNo
			buffer = []string{line}
		} else {
			buffer = append(buffer, line)
		}
	}
	flush(len(lines))
	return splitLargeSpans(spans)
}

func plainSpans(lines []string) []span {
	var spans []span
	for offset := 0; offset < len(lines); offset += 80 {
		end := min(offset+80, len(lines))
		spans = append(spans, span{Start: offset + 1, End: end, Content: strings.Join(lines[offset:end], "\n")})
	}
	return splitLargeSpans(spans)
}

func splitLargeSpans(spans []span) []span {
	var result []span
	for _, sp := range spans {
		if len(sp.Content) <= 2200 {
			result = append(result, sp)
			continue
		}
		lines := strings.Split(sp.Content, "\n")
		for offset := 0; offset < len(lines); offset += 45 {
			end := min(offset+45, len(lines))
			result = append(result, span{
				Start:   sp.Start + offset,
				End:     sp.Start + end - 1,
				Heading: sp.Heading,
				Content: strings.Join(lines[offset:end], "\n"),
			})
		}
	}
	return result
}

func (idx *Index) Search(ctx context.Context, query string, limit int, filters map[string]string) ([]SearchResult, error) {
	expanded := ExpandQuery(query)
	db, err := idx.Connect()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := idx.ftsRows(db, expanded, limit*4, filters)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		rows, err = idx.likeRows(db, expanded, limit*4, filters)
		if err != nil {
			return nil, err
		}
	}
	queryEmbedding, _ := idx.AI.Embed(ctx, expanded)
	results := make([]SearchResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, rowToResult(row, expanded, queryEmbedding))
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

type chunkRow struct {
	ID                    int64
	DocumentPath          string
	RepoName              string
	SubmodulePath         string
	CommitSHA             string
	BranchOrDetachedState string
	Heading               string
	LineStart             int
	LineEnd               int
	DocClassification     string
	SourceLayer           string
	Content               string
	ContentHash           string
	EmbeddingJSON         sql.NullString
	Rank                  float64
}

func (idx *Index) ftsRows(db *sql.DB, query string, limit int, filters map[string]string) ([]chunkRow, error) {
	terms := Tokenize(query)
	if len(terms) == 0 {
		return nil, nil
	}
	ftsQuery := strings.Join(terms[:min(12, len(terms))], " OR ")
	where, params := filterClause(filters)
	sqlText := fmt.Sprintf(`
select c.id, c.document_path, c.repo_name, c.submodule_path, c.commit_sha, c.branch_or_detached_state,
       c.heading, c.line_start, c.line_end, c.doc_classification, c.source_layer, c.content,
       c.content_hash, c.embedding_json, bm25(chunk_fts) as rank
from chunk_fts
join chunks c on c.id = chunk_fts.rowid
where chunk_fts match ? and c.active = 1 %s
order by rank
limit ?`, where)
	allParams := append([]any{ftsQuery}, params...)
	allParams = append(allParams, limit)
	rows, err := db.Query(sqlText, allParams...)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()
	return scanChunkRows(rows)
}

func (idx *Index) likeRows(db *sql.DB, query string, limit int, filters map[string]string) ([]chunkRow, error) {
	terms := Tokenize(query)
	if len(terms) > 8 {
		terms = terms[:8]
	}
	var clauses []string
	var params []any
	for _, term := range terms {
		clauses = append(clauses, "lower(content) like ? or lower(heading) like ?")
		params = append(params, "%"+strings.ToLower(term)+"%", "%"+strings.ToLower(term)+"%")
	}
	if len(clauses) == 0 {
		clauses = append(clauses, "1=1")
	}
	where, filterParams := filterClause(filters)
	params = append(params, filterParams...)
	params = append(params, limit)
	sqlText := fmt.Sprintf(`
select id, document_path, repo_name, submodule_path, commit_sha, branch_or_detached_state,
       heading, line_start, line_end, doc_classification, source_layer, content,
       content_hash, embedding_json, 10.0 as rank
from chunks
where active = 1 and (%s) %s
limit ?`, strings.Join(clauses, " or "), where)
	rows, err := db.Query(sqlText, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunkRows(rows)
}

func scanChunkRows(rows *sql.Rows) ([]chunkRow, error) {
	var out []chunkRow
	for rows.Next() {
		var row chunkRow
		err := rows.Scan(&row.ID, &row.DocumentPath, &row.RepoName, &row.SubmodulePath, &row.CommitSHA,
			&row.BranchOrDetachedState, &row.Heading, &row.LineStart, &row.LineEnd, &row.DocClassification,
			&row.SourceLayer, &row.Content, &row.ContentHash, &row.EmbeddingJSON, &row.Rank)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func filterClause(filters map[string]string) (string, []any) {
	if filters == nil {
		return "", nil
	}
	columns := map[string]string{"repo": "repo_name", "source_layer": "source_layer", "classification": "doc_classification"}
	var clauses []string
	var params []any
	for _, key := range []string{"repo", "source_layer", "classification"} {
		if value := filters[key]; value != "" {
			clauses = append(clauses, "and "+columns[key]+" = ?")
			params = append(params, value)
		}
	}
	return strings.Join(clauses, " "), params
}

func rowToResult(row chunkRow, query string, queryEmbedding []float64) SearchResult {
	base := 1.0 / (1.0 + math.Abs(row.Rank))
	lexical := LexicalScore(query, row.Heading+" "+row.Content)
	authority := AuthorityWeight(row.DocClassification, row.SourceLayer, row.DocumentPath)
	scopePenalty := 0.0
	if strings.Contains(strings.ToLower(row.Heading), "out of scope") && !strings.Contains(strings.ToLower(query), "scope") {
		scopePenalty = -0.9
	}
	vector := 0.0
	if len(queryEmbedding) > 0 && row.EmbeddingJSON.Valid {
		var embedding []float64
		if err := json.Unmarshal([]byte(row.EmbeddingJSON.String), &embedding); err == nil {
			vector = Cosine(queryEmbedding, embedding)
		}
	}
	return SearchResult{
		ChunkID:               row.ID,
		Score:                 base + lexical + authority + vector + scopePenalty,
		Content:               row.Content,
		Heading:               row.Heading,
		RepoName:              row.RepoName,
		FilePath:              row.DocumentPath,
		LineStart:             row.LineStart,
		LineEnd:               row.LineEnd,
		DocClassification:     row.DocClassification,
		SourceLayer:           row.SourceLayer,
		CommitSHA:             row.CommitSHA,
		BranchOrDetachedState: row.BranchOrDetachedState,
	}
}

func (idx *Index) Query(ctx context.Context, query string, filters map[string]string, limit int) (QueryResponse, error) {
	results, err := idx.Search(ctx, query, limit, filters)
	if err != nil {
		return QueryResponse{}, err
	}
	citations := make([]Citation, len(results))
	for i, result := range results {
		citations[i] = result.Citation()
	}
	conflicts := DetectConflicts(results)
	localAnswer := ComposeAnswer(query, results, conflicts)
	generated, usedLLM := idx.AI.Generate(ctx, AnswerPrompt(query, results, conflicts))
	answer := localAnswer
	if usedLLM {
		answer = generated
	}
	return QueryResponse{
		Query:           query,
		Answer:          answer,
		Citations:       citations,
		MatchedChunks:   results,
		Conflicts:       conflicts,
		ConfidenceNotes: ConfidenceNotes(results, usedLLM),
	}, nil
}

func (idx *Index) Status() (StatusResponse, error) {
	db, err := idx.Connect()
	if err != nil {
		return StatusResponse{}, err
	}
	defer db.Close()
	idx.repos = DiscoverRepositories(idx.Workspace)
	if err := idx.updateRepositories(db); err != nil {
		return StatusResponse{}, err
	}
	rows, err := db.Query("select path, name, commit_sha, branch_or_detached_state, dirty, indexed_at from repositories order by path")
	if err != nil {
		return StatusResponse{}, err
	}
	var repos []StatusRepository
	for rows.Next() {
		var repo StatusRepository
		if err := rows.Scan(&repo.Path, &repo.Name, &repo.CommitSHA, &repo.BranchOrDetachedState, &repo.Dirty, &repo.IndexedAt); err != nil {
			rows.Close()
			return StatusResponse{}, err
		}
		repos = append(repos, repo)
	}
	rows.Close()
	var activeDocuments, activeChunks int
	_ = db.QueryRow("select count(*) from documents where active = 1").Scan(&activeDocuments)
	_ = db.QueryRow("select count(*) from chunks where active = 1").Scan(&activeChunks)
	var last sql.NullFloat64
	_ = db.QueryRow("select max(indexed_at) from documents").Scan(&last)
	var lastPtr *float64
	if last.Valid {
		lastPtr = &last.Float64
	}
	var dirty []StatusRepository
	for _, repo := range repos {
		if repo.Dirty != 0 {
			dirty = append(dirty, repo)
		}
	}
	return StatusResponse{
		Workspace:         idx.Workspace,
		DBPath:            idx.DBPath,
		ActiveDocuments:   activeDocuments,
		ActiveChunks:      activeChunks,
		LastIndexedAt:     lastPtr,
		Repositories:      repos,
		DirtyRepositories: dirty,
	}, nil
}
