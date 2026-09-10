package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/deps"
	"github.com/yshah-aromatech/scriptorium/internal/scripts"
	"github.com/yshah-aromatech/scriptorium/internal/secret"
)

func TestCancellationStopsPreparationDescendants(t *testing.T) {
	for _, path := range []string{"task", "powershell-scan", "python-scan", "sync"} {
		t.Run(path, func(t *testing.T) {
			dir := t.TempDir()
			parentPID, childPID := filepath.Join(dir, "parent"), filepath.Join(dir, "child")
			gate, effect := filepath.Join(dir, "gate"), filepath.Join(dir, "effect")
			stub := filepath.Join(dir, "git")
			write(t, stub, fmt.Sprintf(`#!/bin/sh
echo $$ > %q
/bin/sh -c 'echo $$ > "$1"; while [ ! -e "$2" ]; do sleep 0.01; done; echo survived > "$3"; exec sleep 30' child %q %q %q &
wait
`, parentPID, childPID, gate, effect))
			if err := os.Chmod(stub, 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			scanner := &deps.Scanner{PwshBin: stub}
			entry := filepath.Join(dir, "entry.ps1")
			write(t, entry, "# fixture")
			venv := filepath.Join(dir, "venv")
			finished := make(chan error, 1)
			go func() {
				var err error
				switch path {
				case "task":
					if !streamCmd(ctx, secret.NewRegistry(), func(string) {}, stub) {
						err = errors.New("cancelled task")
					}
				case "powershell-scan":
					_, err = scanner.ScanPSContext(ctx, entry, dir, "", false)
				case "python-scan":
					_, err = scanner.ScanPythonContext(ctx, dir, venv, stub)
				case "sync":
					if !scripts.SyncOne(ctx, scripts.Repo{Name: "fixture", URL: "unused", Root: filepath.Join(dir, "repo")}, secret.NewRegistry(), func(string) {}) {
						err = errors.New("cancelled sync")
					}
				}
				finished <- err
			}()
			waitFile(t, childPID)
			pidFrom := func(path string) int {
				b, _ := os.ReadFile(path)
				pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
				return pid
			}
			parent, child := pidFrom(parentPID), pidFrom(childPID)
			defer func() {
				if parent > 0 {
					_ = syscall.Kill(parent, syscall.SIGKILL)
				}
				if child > 0 {
					_ = syscall.Kill(child, syscall.SIGKILL)
				}
			}()
			cancel()
			// Wait for the parent to be reaped before releasing the child's work;
			// fixed sleeps could mistake scheduler delay for broken cancellation.
			deadline := time.Now().Add(2 * time.Second)
			for syscall.Kill(parent, 0) == nil && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			write(t, gate, "release")
			select {
			case err := <-finished:
				if err == nil {
					t.Error("cancelled subprocess reported success")
				}
			case <-time.After(2 * time.Second):
				_ = syscall.Kill(child, syscall.SIGKILL)
				_ = syscall.Kill(parent, syscall.SIGKILL)
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					t.Fatal("fixture failed to clean up")
				}
				t.Error("cancel left a descendant holding output pipes")
			}
			if _, err := os.Stat(effect); err == nil {
				t.Error("descendant performed a side effect after cancellation")
			}
		})
	}
}
