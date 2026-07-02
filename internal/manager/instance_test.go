package manager

import (
	"bytes"
	"testing"
)

func TestPrefixWriterCompleteLines(t *testing.T) {
	var buf bytes.Buffer
	w := newPrefixWriter(&buf, "[a] ")

	n, err := w.Write([]byte("hello\nworld\n"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 12 {
		t.Errorf("n = %d, want 12", n)
	}
	if got, want := buf.String(), "[a] hello\n[a] world\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPrefixWriterBuffersPartialLine(t *testing.T) {
	var buf bytes.Buffer
	w := newPrefixWriter(&buf, "[a] ")

	// A partial line must not be emitted until its newline arrives.
	w.Write([]byte("par"))
	if buf.Len() != 0 {
		t.Errorf("partial line emitted early: %q", buf.String())
	}
	w.Write([]byte("tial\n"))
	if got, want := buf.String(), "[a] partial\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPrefixWriterMultipleLinesOneWrite(t *testing.T) {
	var buf bytes.Buffer
	w := newPrefixWriter(&buf, "> ")

	w.Write([]byte("one\ntwo\nthree"))
	// "three" has no newline yet, so only the first two lines appear.
	if got, want := buf.String(), "> one\n> two\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	w.Write([]byte("\n"))
	if got, want := buf.String(), "> one\n> two\n> three\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
