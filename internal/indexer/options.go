package indexer

// Option configures an Indexer.
type Option func(*Indexer)

// WithAllowExtensions replaces the default extension allowlist. Extensions must
// include the leading dot and be lowercase (e.g. ".go").
func WithAllowExtensions(exts ...string) Option {
	return func(ix *Indexer) { ix.allowExts = set(exts...) }
}

// WithIgnoreDirs replaces the default set of directory names to skip.
func WithIgnoreDirs(dirs ...string) Option {
	return func(ix *Indexer) { ix.ignoreDirs = set(dirs...) }
}

// WithMaxFileSize sets the maximum file size (bytes) to index; larger files are
// skipped. A non-positive value is ignored.
func WithMaxFileSize(n int64) Option {
	return func(ix *Indexer) {
		if n > 0 {
			ix.maxFileSize = n
		}
	}
}

// WithBatchSize sets how many chunks are embedded per Embed call. A
// non-positive value is ignored.
func WithBatchSize(n int) Option {
	return func(ix *Indexer) {
		if n > 0 {
			ix.batchSize = n
		}
	}
}
