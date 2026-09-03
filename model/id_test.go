package model

import (
	"strconv"
	"testing"
)

func TestFormatID(t *testing.T) {
	if got := FormatID(1); got != "1" {
		t.Fatalf("FormatID(1) = %q, want 1", got)
	}
	if got := FormatID(12); got != "12" {
		t.Fatalf("FormatID(12) = %q, want 12", got)
	}
	if FormatID(0) != "" || FormatID(-3) != "" {
		t.Fatal("FormatID must reject non-positive seq")
	}
}

func TestParseID(t *testing.T) {
	n, ok := ParseID("42")
	if !ok || n != 42 {
		t.Fatalf("ParseID(42) = %d,%v", n, ok)
	}
	if _, ok := ParseID("alice-c0001"); ok {
		t.Fatal("legacy concat must not parse as numeric")
	}
	if _, ok := ParseID("01"); !ok {
		// "01" is still decimal digits; ParseID accepts it as 1
		t.Fatal("zero-padded digits are still numeric")
	}
	if n, ok := ParseID("01"); !ok || n != 1 {
		t.Fatalf("ParseID(01) = %d,%v, want 1,true", n, ok)
	}
}

func TestSeqFromID_LegacyAndNumeric(t *testing.T) {
	cases := map[string]int{
		"1":                       1,
		"12":                      12,
		"alice-c0001":             1,
		"alice-core-s0002":        2,
		"alice-core-s0001-m0003":  3,
		"alice-core-s0001-t0004":  4,
		"alice-core-s0001-f0005":  5,
		"alice-core-s0001-uf0006": 6,
		"alice-core-s0001-l0007":  7,
		"alice-core-s0001-rt0008": 8,
		"not-an-id":               0,
	}
	for id, want := range cases {
		if got := SeqFromID(id); got != want {
			t.Errorf("SeqFromID(%q) = %d, want %d", id, got, want)
		}
	}
}

func TestGenerateUserID(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 32; i++ {
		id := GenerateUserID()
		if !IsGeneratedUserID(id) {
			t.Fatalf("GenerateUserID() = %q, want 8-digit in [%d,%d]", id, userIDMin, userIDMax)
		}
		if len(id) != 8 {
			t.Fatalf("GenerateUserID() length = %d, want 8", len(id))
		}
		n, _ := strconv.Atoi(id)
		if n < userIDMin || n > userIDMax {
			t.Fatalf("GenerateUserID() = %d out of range", n)
		}
		seen[id] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatal("GenerateUserID should not collapse to a constant")
	}
}

func TestDisplayID(t *testing.T) {
	cases := map[string]string{
		"1":                       "1",
		"01":                      "1",
		"12":                      "12",
		"alice-c0001":             "1",
		"alice-core-s0002":        "2",
		"alice-core-s0001-m0003":  "3",
		"alice-core-s0001-t0004":  "4",
		"alice-core-s0001-uf0006": "6",
		"call_abc":                "call_abc",
		"alice":                   "alice",
		"user-1":                  "user-1",
		"48291037":                "48291037",
		"":                        "",
	}
	for id, want := range cases {
		if got := DisplayID(id); got != want {
			t.Errorf("DisplayID(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestEnsureID(t *testing.T) {
	if got := EnsureID("7", 3); got != "7" {
		t.Fatalf("EnsureID keeps existing = %q", got)
	}
	if got := EnsureID("", 3); got != "3" {
		t.Fatalf("EnsureID fills empty = %q", got)
	}
	if got := EnsureID("alice-c0001", 1); got != "alice-c0001" {
		t.Fatal("EnsureID must not rewrite legacy ids")
	}
}

func TestIsLegacyConcatID(t *testing.T) {
	if !IsLegacyConcatID("alice-c0001") || !IsLegacyConcatID("u-core-s0001-m0001") {
		t.Fatal("expected concat ids to be legacy")
	}
	if IsLegacyConcatID("1") || IsLegacyConcatID("48291037") || IsLegacyConcatID("") {
		t.Fatal("numeric ids are not legacy concat")
	}
}
