package chunker

import (
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tree_sitter_javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/vaughanb/agent-context-go/internal/core"
)

// language binds a tree-sitter grammar to the metadata the chunker needs.
// unwrapKinds are wrapper node kinds whose real declaration is their last
// named child (e.g. a decorator or export wrapping a function); the chunker
// descends through them to recover the declaration's name.
type language struct {
	name        string
	tsLang      *tree_sitter.Language
	unwrapKinds map[string]bool
}

// builtinLanguages maps file extensions to their language definition. Several
// extensions may share one grammar.
func builtinLanguages() map[string]*language {
	goLang := &language{
		name:        "go",
		tsLang:      tree_sitter.NewLanguage(tree_sitter_go.Language()),
		unwrapKinds: set("type_declaration"),
	}
	pyLang := &language{
		name:        "python",
		tsLang:      tree_sitter.NewLanguage(tree_sitter_python.Language()),
		unwrapKinds: set("decorated_definition"),
	}
	jsLang := &language{
		name:        "javascript",
		tsLang:      tree_sitter.NewLanguage(tree_sitter_javascript.Language()),
		unwrapKinds: set("export_statement"),
	}
	tsLang := &language{
		name:        "typescript",
		tsLang:      tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript()),
		unwrapKinds: set("export_statement"),
	}
	tsxLang := &language{
		name:        "tsx",
		tsLang:      tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTSX()),
		unwrapKinds: set("export_statement"),
	}

	return map[string]*language{
		".go":  goLang,
		".py":  pyLang,
		".js":  jsLang,
		".jsx": jsLang,
		".mjs": jsLang,
		".cjs": jsLang,
		".ts":  tsLang,
		".tsx": tsxLang,
	}
}

// astChunks parses src and emits one chunk per top-level declaration,
// line-splitting any declaration that exceeds maxLines. It returns nil if the
// parse yields no usable tree, letting the caller fall back to line chunking.
func (s *Service) astChunks(src []byte, lang *language) []core.Chunk {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(lang.tsLang); err != nil {
		return nil
	}

	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil
	}
	defer tree.Close()

	root := tree.RootNode()
	if root == nil {
		return nil
	}

	var chunks []core.Chunk
	n := root.NamedChildCount()
	for i := uint(0); i < n; i++ {
		child := root.NamedChild(i)
		if child == nil {
			continue
		}
		chunks = append(chunks, s.chunkNode(child, src, lang)...)
	}
	return s.mergeTrivial(chunks)
}

// chunkNode turns a single top-level node into one chunk, or several when the
// node spans more than maxLines.
func (s *Service) chunkNode(node *tree_sitter.Node, src []byte, lang *language) []core.Chunk {
	startLine := int(node.StartPosition().Row) + 1
	endLine := int(node.EndPosition().Row) + 1
	symbol := lang.symbolOf(node, src)
	text := node.Utf8Text(src)

	if endLine-startLine+1 <= s.maxLines {
		return []core.Chunk{{
			StartLine: startLine,
			EndLine:   endLine,
			Symbol:    symbol,
			Content:   text,
		}}
	}
	return s.lineChunks([]byte(text), startLine, symbol)
}

// symbolOf returns the declaration name for node, descending through wrapper
// nodes as needed. It returns "" when there is no obvious single name.
func (l *language) symbolOf(node *tree_sitter.Node, src []byte) string {
	cur := node
	for depth := 0; depth < 5 && cur != nil; depth++ {
		if name := cur.ChildByFieldName("name"); name != nil {
			return name.Utf8Text(src)
		}
		if !l.unwrapKinds[cur.Kind()] {
			break
		}
		cur = lastNamedChild(cur)
	}
	return ""
}

func lastNamedChild(node *tree_sitter.Node) *tree_sitter.Node {
	n := node.NamedChildCount()
	if n == 0 {
		return nil
	}
	return node.NamedChild(n - 1)
}

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}
