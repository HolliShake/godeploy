package main

import "testing"

func TestTerminalViewContentIncludesCompletedAndPartialLines(t *testing.T) {
	terminal := &terminalView{
		lines: []string{"first", "second"},
		cur:   []byte("partial"),
	}

	if got, want := terminal.Content(), "first\nsecond\npartial"; got != want {
		t.Errorf("Content() = %q, want %q", got, want)
	}
}
