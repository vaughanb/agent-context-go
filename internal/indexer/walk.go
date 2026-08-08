package indexer

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// candidate is a file selected for indexing, with the metadata needed to
// upsert it. rel is the slash-separated path relative to the codebase root, so
// an index is stable across platforms and machines.
type candidate struct {
	abs         string
	rel         string
	size        int64
	modTimeUnix int64
}

// walk traverses absRoot, returning the files eligible for indexing after
// applying the ignore-directory set, extension allowlist, and size limit.
func (ix *Indexer) walk(absRoot string) ([]candidate, error) {
	var out []candidate
	err := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip unreadable entries rather than aborting the whole walk.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != absRoot && ix.shouldSkipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !ix.allowExts[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() == 0 || info.Size() > ix.maxFileSize {
			return nil
		}
		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			return nil
		}
		out = append(out, candidate{
			abs:         path,
			rel:         filepath.ToSlash(rel),
			size:        info.Size(),
			modTimeUnix: info.ModTime().Unix(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// shouldSkipDir reports whether a directory should be pruned from the walk. It
// skips the configured ignore set and any dot-directory (e.g. .git, .idea).
func (ix *Indexer) shouldSkipDir(name string) bool {
	if ix.ignoreDirs[name] {
		return true
	}
	return strings.HasPrefix(name, ".") && name != "."
}

// defaultIgnoreDirs are directory names never worth indexing: VCS metadata,
// dependency caches, build outputs, and virtualenvs.
func defaultIgnoreDirs() map[string]bool {
	return set(
		".git", ".hg", ".svn",
		"node_modules", "vendor", "bower_components",
		"dist", "build", "out", "target", "bin", "obj",
		".venv", "venv", "__pycache__", ".mypy_cache", ".pytest_cache",
		".next", ".nuxt", ".cache", ".idea", ".vscode",
		// Unity generated/cache dirs. Library (esp. Library/PackageCache) holds
		// the full C# source of every imported package — thousands of .cs files
		// that would otherwise be embedded alongside the project's own code.
		"Library", "Temp", "Logs", "Builds", "MemoryCaptures", "UserSettings",
	)
}

// defaultAllowExts are the file extensions indexed by default: source files
// (tree-sitter-chunked or line-chunked) plus common docs and config.
func defaultAllowExts() map[string]bool {
	return set(
		".go", ".py", ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx",
		".java", ".kt", ".scala", ".rs", ".rb", ".php", ".cs",
		".c", ".h", ".cc", ".cpp", ".hpp", ".cxx",
		".swift", ".m", ".mm", ".sh", ".bash", ".ps1",
		".sql", ".proto", ".graphql",
		".html", ".css", ".scss",
		".md", ".rst", ".txt",
		".json", ".yaml", ".yml", ".toml", ".ini",
	)
}

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}
