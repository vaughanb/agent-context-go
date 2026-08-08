package chunker

import (
	"context"
	"strings"
	"testing"

	"github.com/vaughanb/agent-context-go/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func symbols(chunks []core.Chunk) []string {
	var out []string
	for _, c := range chunks {
		if c.Symbol != "" {
			out = append(out, c.Symbol)
		}
	}
	return out
}

func TestChunkEmptyAndUnsupported(t *testing.T) {
	ctx := context.Background()
	s := New()

	empty, err := s.Chunk(ctx, "a.go", nil)
	require.NoError(t, err)
	assert.Nil(t, empty)

	// Unknown extension falls back to line chunking with no symbols.
	txt := []byte("plain text line one\nplain text line two\n")
	chunks, err := s.Chunk(ctx, "notes.txt", txt)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, "", chunks[0].Symbol)
	assert.Equal(t, 1, chunks[0].StartLine)
}

func TestChunkExtractsSymbols(t *testing.T) {
	ctx := context.Background()
	s := New()

	testCases := map[string]struct {
		path        string
		src         string
		wantSymbols []string
	}{
		"go functions, method, and type": {
			path: "svc.go",
			src: `package svc

import "fmt"

type Server struct {
	addr string
}

func New(addr string) *Server {
	return &Server{addr: addr}
}

func (s *Server) Serve() error {
	fmt.Println(s.addr)
	return nil
}
`,
			wantSymbols: []string{"Server", "New", "Serve"},
		},
		"python def and class": {
			path: "m.py",
			src: `import os


def parse_config(path):
    return os.path.exists(path)


class Handler:
    def run(self):
        return 1
`,
			wantSymbols: []string{"parse_config", "Handler"},
		},
		"typescript interface, type, class": {
			path: "m.ts",
			src: `export interface User {
  id: number;
}

type ID = string;

export class Repo {
  find(id: ID): User | null {
    return null;
  }
}
`,
			wantSymbols: []string{"User", "ID", "Repo"},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			chunks, err := s.Chunk(ctx, tc.path, []byte(tc.src))
			require.NoError(t, err)
			require.NotEmpty(t, chunks)
			got := symbols(chunks)
			for _, want := range tc.wantSymbols {
				assert.Contains(t, got, want)
			}
			// Line ranges must be sane and 1-based.
			for _, c := range chunks {
				assert.GreaterOrEqual(t, c.StartLine, 1)
				assert.GreaterOrEqual(t, c.EndLine, c.StartLine)
			}
		})
	}
}

func TestOversizedDeclarationIsSplit(t *testing.T) {
	ctx := context.Background()
	s := New(WithMaxLines(5), WithOverlap(1))

	var b strings.Builder
	b.WriteString("package big\n\nfunc Huge() {\n")
	for i := 0; i < 30; i++ {
		b.WriteString("\tprintln(1)\n")
	}
	b.WriteString("}\n")

	chunks, err := s.Chunk(ctx, "big.go", []byte(b.String()))
	require.NoError(t, err)

	var huge []core.Chunk
	for _, c := range chunks {
		if c.Symbol == "Huge" {
			huge = append(huge, c)
		}
	}
	require.Greater(t, len(huge), 1, "oversized function should split into multiple chunks")
	for _, c := range huge {
		assert.LessOrEqual(t, c.EndLine-c.StartLine+1, 5)
	}
}

func TestLineChunksOverlap(t *testing.T) {
	s := New(WithMaxLines(3), WithOverlap(1))
	lines := "l1\nl2\nl3\nl4\nl5\nl6\n"

	chunks := s.lineChunks([]byte(lines), 1, "")
	require.GreaterOrEqual(t, len(chunks), 2)
	// step = maxLines - overlap = 2; second window starts at line 3.
	assert.Equal(t, 1, chunks[0].StartLine)
	assert.Equal(t, 3, chunks[0].EndLine)
	assert.Equal(t, 3, chunks[1].StartLine)
}

func TestMergeTrivialFoldsSmallSymbolless(t *testing.T) {
	s := New()
	in := []core.Chunk{
		{StartLine: 1, EndLine: 1, Content: "package svc"},
		{StartLine: 3, EndLine: 3, Content: `import "fmt"`},
		{StartLine: 5, EndLine: 12, Symbol: "Foo", Content: "func Foo() {}"},
	}
	out := s.mergeTrivial(in)
	require.Len(t, out, 2)
	assert.Equal(t, "package svc\nimport \"fmt\"", out[0].Content)
	assert.Equal(t, 1, out[0].StartLine)
	assert.Equal(t, 3, out[0].EndLine)
	assert.Equal(t, "Foo", out[1].Symbol)
}
