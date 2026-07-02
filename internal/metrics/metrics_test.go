package metrics

import (
	"strings"
	"testing"
)

func TestRelabelInjectsModel(t *testing.T) {
	in := "# HELP foo help text\n" +
		"# TYPE foo counter\n" +
		"foo 42\n" +
		"bar{le=\"0.5\"} 7\n"
	out := relabel(in, "gemma")

	// Comments untouched.
	if !strings.Contains(out, "# HELP foo help text") {
		t.Errorf("comment altered: %s", out)
	}
	// Label added to a plain sample.
	if !strings.Contains(out, `foo{model="gemma"} 42`) {
		t.Errorf("plain sample not labelled: %s", out)
	}
	// Label merged into an existing label set.
	if !strings.Contains(out, `bar{le="0.5",model="gemma"} 7`) {
		t.Errorf("labelled sample not merged: %s", out)
	}
}

func TestRelabelEmpty(t *testing.T) {
	if out := relabel("", "m"); strings.TrimSpace(out) != "" {
		t.Errorf("empty input should stay empty, got %q", out)
	}
}
