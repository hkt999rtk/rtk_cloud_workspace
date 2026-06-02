package rag

type RepositoryInfo struct {
	Name                  string `json:"name"`
	Path                  string `json:"path"`
	CommitSHA             string `json:"commit_sha"`
	BranchOrDetachedState string `json:"branch_or_detached_state"`
	Dirty                 bool   `json:"dirty"`
}

type Chunk struct {
	Content               string
	Heading               string
	WorkspacePath         string
	RepoName              string
	SubmodulePath         string
	CommitSHA             string
	BranchOrDetachedState string
	FilePath              string
	LineStart             int
	LineEnd               int
	DocClassification     string
	SourceLayer           string
	ContentHash           string
}

type SearchResult struct {
	ChunkID               int64   `json:"chunk_id"`
	Score                 float64 `json:"score"`
	Content               string  `json:"content"`
	Heading               string  `json:"heading"`
	RepoName              string  `json:"repo_name"`
	FilePath              string  `json:"file_path"`
	LineStart             int     `json:"line_start"`
	LineEnd               int     `json:"line_end"`
	DocClassification     string  `json:"doc_classification"`
	SourceLayer           string  `json:"source_layer"`
	CommitSHA             string  `json:"commit_sha"`
	BranchOrDetachedState string  `json:"branch_or_detached_state"`
}

type Citation struct {
	Repo           string  `json:"repo"`
	Path           string  `json:"path"`
	Lines          [2]int  `json:"lines"`
	Commit         string  `json:"commit"`
	Heading        string  `json:"heading"`
	Classification string  `json:"classification"`
	SourceLayer    string  `json:"source_layer"`
	Score          float64 `json:"score"`
}

func (r SearchResult) Citation() Citation {
	return Citation{
		Repo:           r.RepoName,
		Path:           r.FilePath,
		Lines:          [2]int{r.LineStart, r.LineEnd},
		Commit:         r.CommitSHA,
		Heading:        r.Heading,
		Classification: r.DocClassification,
		SourceLayer:    r.SourceLayer,
		Score:          round4(r.Score),
	}
}

type Conflict struct {
	Type    string   `json:"type"`
	Message string   `json:"message"`
	Paths   []string `json:"paths"`
}

type QueryResponse struct {
	Query           string         `json:"query"`
	Answer          string         `json:"answer"`
	Citations       []Citation     `json:"citations"`
	MatchedChunks   []SearchResult `json:"matched_chunks"`
	Conflicts       []Conflict     `json:"conflicts"`
	ConfidenceNotes []string       `json:"confidence_notes"`
}

type IndexResult struct {
	Indexed     int `json:"indexed"`
	Skipped     int `json:"skipped"`
	ActiveFiles int `json:"active_files"`
}

type StatusRepository struct {
	Path                  string  `json:"path"`
	Name                  string  `json:"name"`
	CommitSHA             string  `json:"commit_sha"`
	BranchOrDetachedState string  `json:"branch_or_detached_state"`
	Dirty                 int     `json:"dirty"`
	IndexedAt             float64 `json:"indexed_at"`
}

type StatusResponse struct {
	Workspace         string             `json:"workspace"`
	DBPath            string             `json:"db_path"`
	ActiveDocuments   int                `json:"active_documents"`
	ActiveChunks      int                `json:"active_chunks"`
	LastIndexedAt     *float64           `json:"last_indexed_at"`
	Repositories      []StatusRepository `json:"repositories"`
	DirtyRepositories []StatusRepository `json:"dirty_repositories"`
}
