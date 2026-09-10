package cron_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/cron"
)

func TestCrontabStderrFailureCannotWrite(t *testing.T) {
	for _, diagnostic := range []string{"crontab: permission denied", "", "no crontab for tester\ncrontab: spool failure"} {
		t.Run(diagnostic, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "written")
			body := "#!/bin/sh\nif [ \"$1\" = -l ]; then\n printf '%s\\n' \"$DIAGNOSTIC\" >&2\n exit 1\nfi\nprintf written > \"$MARKER\"\n"
			if err := os.WriteFile(filepath.Join(dir, "crontab"), []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			t.Setenv("DIAGNOSTIC", diagnostic)
			t.Setenv("MARKER", marker)
			c := &cron.Crontab{}
			err := c.Set("job", "* * * * *")
			if err == nil {
				t.Error("failed read allowed a write")
			} else if diagnostic != "" && !strings.Contains(err.Error(), diagnostic) {
				t.Errorf("diagnostic lost: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Error("crontab was written")
			}
		})
	}
}

func TestKnownAbsentCrontabCanInitialize(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "written")
	if err := os.WriteFile(filepath.Join(dir, "crontab"), []byte("#!/bin/sh\nif [ \"$1\" = -l ]; then\n echo 'no crontab for tester' >&2\n exit 1\nfi\nprintf written > \"$MARKER\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("MARKER", marker)
	if err := (&cron.Crontab{}).Set("job", "* * * * *"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
}
