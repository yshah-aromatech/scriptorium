package history_test

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yshah-aromatech/scriptorium/internal/history"
	"github.com/yshah-aromatech/scriptorium/internal/pwshtest"
	"github.com/yshah-aromatech/scriptorium/internal/retention"
)

// The child announces readiness over a pipe; the parent owns the shared
// transaction lock while simulating the read/rename part of a prune.
func TestHistoryWriterProcess(t *testing.T) {
	path := os.Getenv("STO_HISTORY_TEST_PATH")
	if path == "" {
		return
	}
	fmt.Println("ready")
	if os.Getenv("STO_HISTORY_TEST_OP") == "prune" {
		if err := retention.Prune(retention.Options{DataDir: filepath.Dir(path), HistoryFile: path, HistoryMaxLines: 1}, true); err != nil {
			t.Fatal(err)
		}
	} else if err := history.NewStore(path).Append(history.Row{Script: "new", Status: "success", StartedAt: history.Stamp(time.Now())}); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryTransactionsWaitAcrossProcesses(t *testing.T) {
	for _, op := range []string{"append", "prune", "ps-append", "ps-prune"} {
		t.Run(op, func(t *testing.T) {
			var pwsh string
			if strings.HasPrefix(op, "ps-") {
				pwsh = pwshtest.RequirePwsh(t)
			}
			path := filepath.Join(t.TempDir(), "history.jsonl")
			old := `{"script":"old","status":"success","startedAt":"2020-01-01T00:00:00Z"}` + "\n"
			if err := os.WriteFile(path, []byte(old), 0600); err != nil {
				t.Fatal(err)
			}
			lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
				t.Fatal(err)
			}
			defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			cmd := exec.Command(os.Args[0], "-test.run=^TestHistoryWriterProcess$")
			cmd.Env = append(os.Environ(), "STO_HISTORY_TEST_PATH="+path, "STO_HISTORY_TEST_OP="+op)
			if pwsh != "" {
				root, err := filepath.Abs("../..")
				if err != nil {
					t.Fatal(err)
				}
				cmd = exec.Command(pwsh, "-NoProfile", "-File", "testdata/history-transaction.ps1", "-Root", root, "-Path", path, "-Operation", op)
				cmd.Env = append(os.Environ(), "N8N_WEBHOOK_URL=")
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() && scanner.Text() != "ready" {
			}
			if scanner.Text() != "ready" {
				t.Fatal("child did not announce transaction")
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				t.Fatalf("%s bypassed transaction lock: %v", op, err)
			case <-time.After(200 * time.Millisecond):
			}
			// Replace the inode while owning the lock; append must open the new one.
			tmp := path + ".replacement"
			if err := os.WriteFile(tmp, []byte(old), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(tmp, path); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("transaction did not resume")
			}
			rows, err := history.NewStore(path).Last(0)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(op, "append") && (len(rows) != 2 || rows[1].Script != "new") {
				t.Fatalf("append lost across replacement: %+v", rows)
			}
			b, _ := os.ReadFile(path)
			if !strings.Contains(string(b), `"script":"old"`) {
				t.Fatal("newest old row lost")
			}
		})
	}
}

func TestAppendWaitsForPruneTransactionAndSurvives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	old := `{"script":"old","status":"success","startedAt":"2020-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(path, []byte(old+old), 0600); err != nil {
		t.Fatal(err)
	}
	atPrune := make(chan struct{})
	resume := make(chan struct{})

	calls := 0
	done := make(chan error, 1)
	defer func() {
		close(resume)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	go func() {
		done <- retention.Prune(retention.Options{DataDir: filepath.Dir(path), HistoryFile: path, Now: func() time.Time {
			calls++
			if calls == 2 {
				close(atPrune)
				<-resume
			}
			return time.Now()
		}}, true)
	}()
	<-atPrune
	cmd := exec.Command(os.Args[0], "-test.run=^TestHistoryWriterProcess$")
	cmd.Env = append(os.Environ(), "STO_HISTORY_TEST_PATH="+path, "STO_HISTORY_TEST_OP=append")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("writer did not start")
	}
	appended := make(chan error, 1)
	go func() { appended <- cmd.Wait() }()
	select {
	case err := <-appended:
		t.Fatalf("append bypassed in-progress prune: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	resume <- struct{}{}
	if err := <-appended; err != nil {
		t.Fatal(err)
	}
	rows, err := history.NewStore(path).Last(0)
	if err != nil || len(rows) != 2 || rows[0].Script != "old" || rows[1].Script != "new" {
		t.Fatalf("prune lost appended row: %+v, %v", rows, err)
	}
}
