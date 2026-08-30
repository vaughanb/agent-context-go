package chunker

import (
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_c_sharp "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
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
// containerKinds are declaration kinds that group members behind a "body"
// child (namespaces, classes, structs). An oversized container is not
// line-split blindly: the chunker emits its header (attributes, declaration
// line, base list) as one chunk and recurses into the body's members,
// qualifying their symbols with the container's name (e.g. "Player.Jump").
// namelessKinds are node kinds whose grammar exposes a "name" field that is
// not a declaration symbol (e.g. C# using directives); leaving them nameless
// lets trivial-chunk merging fold runs of them together.
type language struct {
	name           string
	tsLang         *tree_sitter.Language
	unwrapKinds    map[string]bool
	containerKinds map[string]bool
	namelessKinds  map[string]bool
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
		name:           "python",
		tsLang:         tree_sitter.NewLanguage(tree_sitter_python.Language()),
		unwrapKinds:    set("decorated_definition"),
		containerKinds: set("class_definition"),
	}
	jsLang := &language{
		name:           "javascript",
		tsLang:         tree_sitter.NewLanguage(tree_sitter_javascript.Language()),
		unwrapKinds:    set("export_statement"),
		containerKinds: set("class_declaration"),
	}
	tsLang := &language{
		name:           "typescript",
		tsLang:         tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript()),
		unwrapKinds:    set("export_statement"),
		containerKinds: set("class_declaration"),
	}
	tsxLang := &language{
		name:           "tsx",
		tsLang:         tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTSX()),
		unwrapKinds:    set("export_statement"),
		containerKinds: set("class_declaration"),
	}
	// C# files are almost always one namespace wrapping one class, so without
	// container descent a whole file would collapse into a single blindly
	// line-split declaration. Enums are deliberately not containers: their
	// members are trivial and search best as one unit.
	csLang := &language{
		name:   "csharp",
		tsLang: tree_sitter.NewLanguage(tree_sitter_c_sharp.Language()),
		containerKinds: set(
			"namespace_declaration", "class_declaration", "struct_declaration",
			"interface_declaration", "record_declaration",
			// Preprocessor regions (e.g. Unity's #if UNITY_EDITOR) wrap whole
			// declarations as direct children; descend instead of line-splitting.
			"preproc_if", "preproc_elif", "preproc_else",
		),
		namelessKinds: set("using_directive", "extern_alias_directive"),
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
		".cs":  csLang,
	}
}

// astChunks parses src and emits chunks per declaration, descending into
// container declarations (namespaces, classes) and line-splitting anything
// oversized that cannot be descended into. It returns nil if the parse yields
// no usable tree, letting the caller fall back to line chunking.
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
		chunks = append(chunks, s.chunkNode(child, src, lang, "")...)
	}
	return s.mergeTrivial(chunks)
}

// chunkNode turns one declaration into chunks. prefix qualifies the node's
// symbol with its enclosing containers (e.g. "Namespace.Class"). A node that
// fits within maxLines is one chunk; an oversized container splits into a
// header chunk plus one chunk tree per member; any other oversized node is
// line-split.
func (s *Service) chunkNode(node *tree_sitter.Node, src []byte, lang *language, prefix string) []core.Chunk {
	startLine := int(node.StartPosition().Row) + 1
	endLine := int(node.EndPosition().Row) + 1
	symbol := qualify(prefix, lang.symbolOf(node, src))
	text := node.Utf8Text(src)

	if endLine-startLine+1 <= s.maxLines {
		return []core.Chunk{{
			StartLine: startLine,
			EndLine:   endLine,
			Symbol:    symbol,
			Content:   text,
		}}
	}
	if lang.containerKinds[node.Kind()] {
		// A nameless container (e.g. a preprocessor region) passes the outer
		// prefix through, so members inside it stay qualified by their class.
		childPrefix := symbol
		if childPrefix == "" {
			childPrefix = prefix
		}
		if chunks := s.containerChunks(node, src, lang, symbol, childPrefix); len(chunks) > 0 {
			return chunks
		}
	}
	return s.lineChunks([]byte(text), startLine, symbol)
}

// containerChunks splits an oversized container declaration into a header
// chunk — everything from the node's start to its body (attributes, the
// declaration line, any base list) — followed by the body's members, chunked
// recursively with childPrefix qualifying their symbols. Containers without a
// distinct body (preprocessor regions) hold members as direct children and
// get no header. It returns nil when there is nothing to descend into,
// letting the caller fall back to line splitting.
func (s *Service) containerChunks(node *tree_sitter.Node, src []byte, lang *language, symbol, childPrefix string) []core.Chunk {
	var chunks []core.Chunk
	members := node
	if body := node.ChildByFieldName("body"); body != nil {
		members = body
		startLine := int(node.StartPosition().Row) + 1
		headerEnd := int(body.StartPosition().Row) + 1
		headerText := strings.TrimRight(string(src[node.StartByte():body.StartByte()]), " \t\r\n")
		chunks = s.emitSpan(headerText, startLine, headerEnd, symbol)
	}
	if members.NamedChildCount() == 0 {
		return nil
	}

	n := members.NamedChildCount()
	for i := uint(0); i < n; i++ {
		child := members.NamedChild(i)
		if child == nil {
			continue
		}
		chunks = append(chunks, s.chunkNode(child, src, lang, childPrefix)...)
	}
	return chunks
}

// emitSpan emits text as one chunk, or line-splits it when it exceeds
// maxLines. Blank text yields no chunks.
func (s *Service) emitSpan(text string, startLine, endLine int, symbol string) []core.Chunk {
	if strings.TrimSpace(text) == "" {
		return nil
	}
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

// qualify joins a container prefix and a declaration name into a dotted
// symbol. A nameless declaration stays nameless so trivial-chunk merging
// still folds runs of small unnamed members (fields, using directives).
func qualify(prefix, symbol string) string {
	if prefix == "" || symbol == "" {
		return symbol
	}
	return prefix + "." + symbol
}

// symbolOf returns the declaration name for node, descending through wrapper
// nodes as needed. It returns "" when there is no obvious single name.
func (l *language) symbolOf(node *tree_sitter.Node, src []byte) string {
	if l.namelessKinds[node.Kind()] {
		return ""
	}
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
