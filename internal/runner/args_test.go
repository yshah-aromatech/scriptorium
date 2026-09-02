package runner_test

import (
	"reflect"
	"testing"

	"github.com/yshah-aromatech/scriptorium/internal/runner"
)

func TestSplitArguments(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   \t  ", nil},
		{"simple words", "foo bar", []string{"foo", "bar"}},
		{"quoted phrase", `-Message "hello world"`, []string{"-Message", "hello world"}},
		{"single quotes", `-Message 'hello world'`, []string{"-Message", "hello world"}},
		{"mixed quotes", `a"b c"d 'e f'g`, []string{"ab cd", "e fg"}},
		{"adjacent quote-then-text concatenation", `a"b c"d`, []string{"ab cd"}},
		{"empty quotes yield one empty token", `""`, []string{""}},
		{"empty quotes among words", `foo "" bar`, []string{"foo", "", "bar"}},
		{"leading and trailing spaces", "   foo bar   ", []string{"foo", "bar"}},
		{"tabs and multiple spaces between tokens", "foo\t\t  bar", []string{"foo", "bar"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := runner.SplitArguments(c.text)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("SplitArguments(%q) = %#v, want %#v", c.text, got, c.want)
			}
		})
	}
}
