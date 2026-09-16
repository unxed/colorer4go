package colorer

import (
	"context"
	"testing"
)

func outlineSession(t *testing.T, file string) *Session {
	t.Helper()
	s, err := NewSession(context.Background(), "/base/catalog.xml", verifyConfigsOnHost(t))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.SetHRD("rgb", "default"); err != nil {
		t.Fatalf("SetHRD: %v", err)
	}
	if ok, err := s.SelectType(file, ""); err != nil || !ok {
		t.Fatalf("SelectType(%q): %v, %v", file, ok, err)
	}
	return s
}

// A C function definition is outlined; the call inside its body is not.
func TestLineOutline_CFunctions(t *testing.T) {
	s := outlineSession(t, "a.c")
	lines := []string{
		"int alpha(int x)",
		"{",
		"  return beta(x);",
		"}",
		"static void gamma(void) { }",
	}
	var got []string
	for _, line := range lines {
		if _, _, err := s.ParseLinePairs(line); err != nil {
			t.Fatalf("ParseLinePairs: %v", err)
		}
		items, err := s.LineOutline()
		if err != nil {
			t.Fatalf("LineOutline: %v", err)
		}
		for _, it := range items {
			if it.Error {
				continue
			}
			got = append(got, it.Label(line))
			if it.Region == "" || len(it.Spans) == 0 || it.Start != it.Spans[0].Start {
				t.Errorf("item %+v is incomplete", it)
			}
		}
	}
	t.Logf("outlined: %q", got)
	if len(got) != 2 {
		t.Fatalf("outlined %q, want the two definitions", got)
	}
}
