// Package importlint enforces the spec's dependency rule: domain packages
// stay frontend-free (spec 2026-09-01-go-rebuild-design.md §3).
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

// domainDirs lists every internal package that must never know about a
// frontend. Directories that don't exist yet are skipped — the guard
// activates as phases land them.
var domainDirs = []string{
	"envfile", "secret", "config", "lockfile", "procstat", "cron",
	"history", "webhook", "scripts", "deps", "runner", "missed",
	"retention", "migrate", "app",
}

// forbidden matches by prefix.
var forbidden = []string{
	"charm.land/",
	"github.com/charmbracelet/",
	"github.com/spf13/cobra",
}

func TestDomainPackagesAreFrontendFree(t *testing.T) {
	root := moduleRoot(t)
	for _, dir := range domainDirs {
		abs := filepath.Join(root, "internal", dir)
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
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
				// net/http is allowed only in webhook (client) — handlers live in mcp.
				if p == "net/http" && dir != "webhook" {
					t.Errorf("%s imports net/http — only internal/webhook (client) may; handlers belong in internal/mcp", path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
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
