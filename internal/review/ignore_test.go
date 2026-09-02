package review

import "testing"

func TestParseIgnoreFile(t *testing.T) {
	raw := "# vendor junk\n\nvendor/**\n/node_modules/**\n*.lock\n# trailing\n"
	got := ParseIgnoreFile(raw)
	want := []string{"vendor/**", "node_modules/**", "*.lock"}
	if len(got) != len(want) {
		t.Fatalf("ParseIgnoreFile = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseIgnoreFile[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseIgnoreFileDedupes(t *testing.T) {
	got := ParseIgnoreFile("a.go\na.go\n")
	if len(got) != 1 || got[0] != "a.go" {
		t.Fatalf("ParseIgnoreFile = %#v", got)
	}
}
