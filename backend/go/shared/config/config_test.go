package config

import "testing"

func TestParseCSV(t *testing.T) {
	got := parseCSV("a, b, ,c,,")
	if len(got) != 3 {
		t.Fatalf("expected 3 items, got %d", len(got))
	}
	if got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("unexpected values: %#v", got)
	}
}

func TestParseAnyCSV(t *testing.T) {
	raw := []any{"x", " ", "y"}
	got := parseAnyCSV(raw)
	if len(got) != 2 {
		t.Fatalf("expected 2 items, got %d", len(got))
	}
	if got[0] != "x" || got[1] != "y" {
		t.Fatalf("unexpected values: %#v", got)
	}
}
