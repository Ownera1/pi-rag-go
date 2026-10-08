package model

import "context"

type Block struct {
	Text      string  `json:"text"`
	Section   *string `json:"section"`
	PageStart *int    `json:"pageStart"`
	PageEnd   *int    `json:"pageEnd"`
	LineStart *int    `json:"lineStart"`
	LineEnd   *int    `json:"lineEnd"`
	// Kind separates blocks Merge must not join, e.g. "code" from prose.
	Kind string `json:"kind,omitempty"`
}

type Document struct {
	DOI           string           `json:"doi,omitempty"`
	DocumentKey   string           `json:"documentKey,omitempty"`
	Zotero        *ZoteroReference `json:"zotero,omitempty"`
	Replaces      []string         `json:"-"`
	ID            string           `json:"id"`
	SourcePath    string           `json:"sourcePath,omitempty"`
	Title         string           `json:"title,omitempty"`
	ParserVersion string           `json:"parserVersion"`
	Path          string           `json:"path"`
	Hash          string           `json:"hash"`
	Size          int64            `json:"size"`
	Format        string           `json:"format"`
	Blocks        []Block          `json:"blocks"`

	// SearchPath is Path below the documents root, indexed for keyword search
	// so the absolute prefix every document shares matches nothing.
	SearchPath string `json:"-"`
}

type Chunk struct {
	SourcePath    string  `json:"sourcePath,omitempty"`
	Title         string  `json:"title,omitempty"`
	Format        string  `json:"format,omitempty"`
	ParserVersion string  `json:"parserVersion,omitempty"`
	ID            string  `json:"id"`
	Path          string  `json:"path"`
	Content       string  `json:"content"`
	Hash          string  `json:"hash"`
	Tokens        int     `json:"tokens"`
	LineStart     int     `json:"lineStart"`
	LineEnd       int     `json:"lineEnd"`
	PageStart     *int    `json:"pageStart"`
	PageEnd       *int    `json:"pageEnd"`
	Section       *string `json:"section"`
	ChunkIndex    int     `json:"chunkIndex"`
	// Heading is "title > section", indexed for search but not stored.
	Heading string `json:"-"`
}

type Hit struct {
	Metadata *ZoteroMetadata `json:"metadata,omitempty"`
	Chunk    Chunk           `json:"chunk"`
	BM25     float64         `json:"bm25"`
	Vector   float64         `json:"vector"`
	Hybrid   float64         `json:"hybrid"`
	Rerank   *float64        `json:"rerank,omitempty"`
}

type QueryOptions struct {
	Filter        *MetadataFilter `json:"filter,omitempty"`
	DisableSync   bool            `json:"disable_sync,omitempty"`
	TopK          int             `json:"top_k"`
	CandidateTopK int             `json:"candidate_top_k"`
	Alpha         *float64        `json:"alpha,omitempty"`
	Mode          string          `json:"mode,omitempty"`
	DisableRerank bool            `json:"disable_rerank,omitempty"`
	RequireRerank bool            `json:"require_rerank,omitempty"`
}

type QueryResult struct {
	MetadataSyncedAt string       `json:"metadataSyncedAt,omitempty"`
	Freshness        string       `json:"freshness"`
	Sync             *IndexResult `json:"sync,omitempty"`
	SyncError        string       `json:"syncError,omitempty"`
	Query            string       `json:"query"`
	Hits             []Hit        `json:"hits"`
	Method           string       `json:"method"`
	Degraded         string       `json:"degraded,omitempty"`
	ElapsedMs        float64      `json:"elapsedMs"`
	Usage            QueryUsage   `json:"usage"`
}

// Token counts are estimates; calls count logical provider calls, excluding retries.
type QueryUsage struct {
	EmbeddingCalls           int `json:"embeddingCalls"`
	RerankCalls              int `json:"rerankCalls"`
	EstimatedEmbeddingTokens int `json:"estimatedEmbeddingTokens"`
	EstimatedRerankTokens    int `json:"estimatedRerankTokens"`
}

type FileFailure struct {
	Path  string `json:"path"`
	Stage string `json:"stage"`
	Error string `json:"error"`
}

type CleanupResult struct {
	DryRun   bool     `json:"dryRun"`
	Retained []string `json:"retained"`
	Removed  []string `json:"removed"`
	Skipped  []string `json:"skipped"`
}

type IndexResult struct {
	Removed  int           `json:"removed"`
	Indexed  int           `json:"indexed"`
	Skipped  int           `json:"skipped"`
	Failed   int           `json:"failed"`
	Chunks   int           `json:"chunks"`
	Errors   []string      `json:"errors"`
	Failures []FileFailure `json:"failures"`
}

type Status struct {
	Zotero         *ZoteroStatus `json:"zotero,omitempty"`
	WorkspaceDir   string        `json:"workspaceDir"`
	DocumentsRoot  string        `json:"documentsRoot"`
	StoreDir       string        `json:"storeDir"`
	ReadOnly       bool          `json:"readOnly"`
	Files          int           `json:"files"`
	Chunks         int           `json:"chunks"`
	Vectors        int           `json:"vectors"`
	EmbeddingModel string        `json:"embeddingModel"`
	Dimensions     int           `json:"dimensions"`
	NeedsRebuild   bool          `json:"needsRebuild"`
	RebuildReason  string        `json:"rebuildReason,omitempty"`
	ActiveDB       string        `json:"activeDb"`
	NeedsSync      bool          `json:"needsSync"`
	FailedFiles    []FileFailure `json:"failedFiles"`
	LastSync       *IndexResult  `json:"lastSync,omitempty"`
	LastAttemptAt  string        `json:"lastAttemptAt,omitempty"`
	FreshnessError string        `json:"freshnessError,omitempty"`
}

type ProviderConfig struct {
	Type       string `json:"type"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions,omitempty"`
	BaseURL    string `json:"baseUrl,omitempty"`
	APIKeyEnv  string `json:"apiKeyEnv,omitempty"`
}

type ChunkingConfig struct {
	Mode            string `json:"mode"`
	LegacyTarget    int    `json:"legacyTarget"`
	LegacyMax       int    `json:"legacyMax"`
	LegacyOverlap   int    `json:"legacyOverlap"`
	SemanticMin     int    `json:"semanticMin"`
	SemanticTarget  int    `json:"semanticTarget"`
	SemanticMax     int    `json:"semanticMax"`
	SemanticUnitMax int    `json:"semanticUnitMax"`
}

type IndexingConfig struct {
	Workers            int `json:"workers"`
	SemanticWorkers    int `json:"semanticWorkers"`
	EmbeddingWorkers   int `json:"embeddingWorkers"`
	EmbeddingBatchSize int `json:"embeddingBatchSize"`
}

type Config struct {
	Zotero          *ZoteroConfig  `json:"zotero,omitempty"`
	Documents       string         `json:"documents"`
	Embedding       ProviderConfig `json:"embedding"`
	Reranker        ProviderConfig `json:"reranker"`
	Chunking        ChunkingConfig `json:"chunking"`
	Indexing        IndexingConfig `json:"indexing"`
	ExcludePatterns []string       `json:"excludePatterns"`
	Alpha           float64        `json:"alpha"`
	CandidateTopK   int            `json:"candidateTopK"`
	TopK            int            `json:"topK"`
	HTTPTimeoutMs   int            `json:"httpTimeoutMs"`
	HTTPMaxRetries  int            `json:"httpMaxRetries"`
}

type EmbeddingProvider interface {
	Model() string
	Dimensions() int
	EmbedQuery(context.Context, string) ([]float32, error)
	EmbedDocuments(context.Context, []string) ([][]float32, error)
}

type RerankDoc struct {
	ID   string
	Text string
}

type RerankResult struct {
	ID    string
	Score float64
}

type Reranker interface {
	Model() string
	Rerank(context.Context, string, []RerankDoc, int) ([]RerankResult, error)
}
