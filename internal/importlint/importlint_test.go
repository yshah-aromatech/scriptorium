// Package importlint enforces the spec's dependency rule: domain packages
// stay frontend-free (spec 2026-09-01-go-rebuild-design.md §3). Every
// package under internal/ is guarded except the frontend allowlist below.
package importlint_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// frontends are the only internal/ packages allowed to import TUI/CLI
// frameworks and to own net/http handlers. Everything else under internal/
// — including this package itself — must stay frontend-free.
var frontends = map[string]bool{"tui": true, "cli": true, "mcp": true}

// forbidden matches by prefix.
var forbidden = []string{
	"charm.land/",
	"github.com/charmbracelet/",
	"github.com/spf13/cobra",
}

// netHTTPExceptions are packages (by top-level internal/ dir name) allowed
// to import net/http and its subpackages — leaf client packages, not
// handlers. Handlers belong in internal/mcp (a frontend, exempt above).
var netHTTPExceptions = map[string]bool{"webhook": true, "openrouter": true}

func TestDomainPackagesAreFrontendFree(t *testing.T) {
	root := moduleRoot(t)
	internalDir := filepath.Join(root, "internal")
	err := filepath.WalkDir(internalDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		rel, rerr := filepath.Rel(internalDir, path)
		if rerr != nil {
			return rerr
		}
		top := strings.Split(rel, string(filepath.Separator))[0]
		if frontends[top] {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			t.Errorf("%s: %v", path, perr)
			return nil
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if strings.HasPrefix(p, bad) {
					t.Errorf("%s imports %s — domain packages must not import frontends", path, p)
				}
			}
			// net/http is allowed only in leaf client packages (webhook,
			// openrouter) — handlers live in internal/mcp.
			if (p == "net/http" || strings.HasPrefix(p, "net/http/")) && !netHTTPExceptions[top] {
				t.Errorf("%s imports %s — only internal/webhook and internal/openrouter (clients) may; handlers belong in internal/mcp", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test dir")
		}
		dir = parent
	}
}
