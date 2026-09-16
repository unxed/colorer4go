package colorer

import (
	"context"
	"testing"
)

// Brackets in C are pair regions. They must come back as pairs, in order and
// with the right direction, and must not leak into the ordinary regions,
// where they would be painted on every bracket.
func TestParseLinePairs_ReturnsBracketsInOrder(t *testing.T) {
	s, err := NewSession(context.Background(), "/base/catalog.xml", verifyConfigsOnHost(t))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()
	if err := s.SetHRD("rgb", "default"); err != nil {
		t.Fatalf("SetHRD: %v", err)
	}
	if ok, err := s.SelectType("a.c", ""); err != nil || !ok {
		t.Fatalf("SelectType: %v, %v", ok, err)
	}

	line := "int f(int x) { return a[x]; }"
	regions, pairs, err := s.ParseLinePairs(line)
	if err != nil {
		t.Fatalf("ParseLinePairs: %v", err)
	}
	type want struct {
		start int
		opens bool
	}
	var got []want
	for _, p := range pairs {
		if p.End != p.Start+1 {
			t.Errorf("pair %+v is not one bracket wide", p)
		}
		got = append(got, want{p.Start, p.Opens})
	}
	expected := []want{{5, true}, {11, false}, {13, true}, {23, true}, {25, false}, {28, false}}
	if len(got) != len(expected) {
		t.Fatalf("pairs = %+v, want brackets at %+v", got, expected)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("pair %d = %+v, want %+v", i, got[i], expected[i])
		}
	}

	plain, err := s.ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if len(plain) != len(regions) {
		t.Errorf("ParseLinePairs returned %d regions, ParseLine %d", len(regions), len(plain))
	}
}
