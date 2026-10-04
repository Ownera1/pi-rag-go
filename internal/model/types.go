package model

import "context"

type Block struct {
	Text      string  `json:"text"`
	Section   *string `json:"section"`
	PageStart *int    `json:"pageStart"`
	PageEnd   *int    `json:"pageEnd"`
	LineStart *int    `json:"lineStart"`
	LineEnd   *int    `json:"lineEnd"`
}

type Document struct {
	Path   string  `json:"path"`
	Hash   string  `json:"hash"`
	Size   int64   `json:"size"`
	Format string  `json:"format"`
	Blocks []Block `json:"blocks"`
}

type Chunk struct {
	ID         string  `json:"id"`
	Path       string  `json:"path"`
	Content    string  `json:"content"`
	Hash       string  `json:"hash"`
	Tokens     int     `json:"tokens"`
	LineStart  int     `json:"lineStart"`
	LineEnd    int     `json:"lineEnd"`
	PageStart  *int    `json:"pageStart"`
	PageEnd    *int    `json:"pageEnd"`
	Section    *string `json:"section"`
	ChunkIndex int     `json:"chunkIndex"`
}

type Hit struct {
	Chunk  Chunk    `json:"chunk"`
	BM25   float64  `json:"bm25"`
	Vector float64  `json:"vector"`
	Hybrid float64  `json:"hybrid"`
	Rerank *float64 `json:"rerank,omitempty"`
}

type QueryOptions struct {
	TopK          int      `json:"top_k"`
	CandidateTopK int      `json:"candidate_top_k"`
	Alpha         *float64 `json:"alpha,omitempty"`
	Mode          string   `json:"mode,omitempty"`
	DisableRerank bool     `json:"disable_rerank,omitempty"`
	RequireRerank bool     `json:"require_rerank,omitempty"`
}

type QueryResult struct {
	Query     string     `json:"query"`
	Hits      []Hit      `json:"hits"`
	Method    string     `json:"method"`
	Degraded  string     `json:"degraded,omitempty"`
	ElapsedMs float64    `json:"elapsedMs"`
	Usage     QueryUsage `json:"usage"`
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

type Progress struct {
	Operation   string `json:"operation"`
	Phase       string `json:"phase"`
	Running     bool   `json:"running"`
	Total       int    `json:"total"`
	Processed   int    `json:"processed"`
	Indexed     int    `json:"indexed"`
	Skipped     int    `json:"skipped"`
	Failed      int    `json:"failed"`
	CurrentFile string `json:"currentFile,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	Error       string `json:"error,omitempty"`
}

type CleanupResult struct {
	DryRun   bool     `json:"dryRun"`
	Retained []string `json:"retained"`
	Removed  []string `json:"removed"`
	Skipped  []string `json:"skipped"`
}

type IndexResult struct {
	Indexed  int           `json:"indexed"`
	Skipped  int           `json:"skipped"`
	Failed   int           `json:"failed"`
	Chunks   int           `json:"chunks"`
	Errors   []string      `json:"errors"`
	Failures []FileFailure `json:"failures"`
}

type Status struct {
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
	TrackedPaths   []string      `json:"trackedPaths"`
	FailedFiles    []FileFailure `json:"failedFiles"`
	Progress       Progress      `json:"progress"`
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
	EmbeddingBatchSize int `json:"embeddingBatchSize"`
}

type Config struct {
	Embedding       ProviderConfig `json:"embedding"`
	Reranker        ProviderConfig `json:"reranker"`
	Chunking        ChunkingConfig `json:"chunking"`
	Indexing        IndexingConfig `json:"indexing"`
	TrackedPaths    []string       `json:"trackedPaths"`
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
