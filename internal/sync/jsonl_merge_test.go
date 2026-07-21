package sync

import (
	"strings"
	"testing"
)

func mergeKeys(t *testing.T, data []byte) string {
	t.Helper()
	var ks []string
	for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if l == "" {
			continue
		}
		_, k := parseNode(l, 0)
		ks = append(ks, k)
	}
	return strings.Join(ks, ",")
}

func TestUnionBasicChain(t *testing.T) {
	local := []byte(`{"uuid":"A","parentUuid":null,"timestamp":"2026-07-01T10:00:00Z"}
{"uuid":"B","parentUuid":"A","timestamp":"2026-07-01T10:01:00Z"}`)
	remote := []byte(`{"uuid":"A","parentUuid":null,"timestamp":"2026-07-01T10:00:00Z"}
{"uuid":"B","parentUuid":"A","timestamp":"2026-07-01T10:01:00Z"}
{"uuid":"C","parentUuid":"B","timestamp":"2026-07-01T10:02:00Z"}`)
	out, err := UnionJSONL(local, remote)
	if err != nil {
		t.Fatal(err)
	}
	if got := mergeKeys(t, out); got != "A,B,C" {
		t.Fatalf("got %q want A,B,C", got)
	}
}

func TestUnionDivergentBranches(t *testing.T) {
	local := []byte(`{"uuid":"A","parentUuid":null,"timestamp":"2026-07-01T10:00:00Z"}
{"uuid":"B","parentUuid":"A","timestamp":"2026-07-01T10:01:00Z"}
{"uuid":"X","parentUuid":"B","timestamp":"2026-07-01T10:05:00Z"}`)
	remote := []byte(`{"uuid":"A","parentUuid":null,"timestamp":"2026-07-01T10:00:00Z"}
{"uuid":"B","parentUuid":"A","timestamp":"2026-07-01T10:01:00Z"}
{"uuid":"Y","parentUuid":"B","timestamp":"2026-07-01T10:03:00Z"}`)
	out, err := UnionJSONL(local, remote)
	if err != nil {
		t.Fatal(err)
	}
	if got := mergeKeys(t, out); got != "A,B,Y,X" {
		t.Fatalf("got %q want A,B,Y,X", got)
	}
}

func TestUnionClockSkewParentBeforeChild(t *testing.T) {
	local := []byte(`{"uuid":"P","parentUuid":null,"timestamp":"2026-07-01T10:00:05Z"}
{"uuid":"K","parentUuid":"P","timestamp":"2026-07-01T10:00:01Z"}`)
	out, err := UnionJSONL(local, local)
	if err != nil {
		t.Fatal(err)
	}
	if got := mergeKeys(t, out); got != "P,K" {
		t.Fatalf("got %q want P,K (parent before child despite earlier child ts)", got)
	}
}

func TestUnionIdempotent(t *testing.T) {
	local := []byte(`{"uuid":"A","parentUuid":null,"timestamp":"2026-07-01T10:00:00Z"}
{"uuid":"B","parentUuid":"A","timestamp":"2026-07-01T10:01:00Z"}`)
	remote := []byte(`{"uuid":"A","parentUuid":null,"timestamp":"2026-07-01T10:00:00Z"}
{"uuid":"C","parentUuid":"A","timestamp":"2026-07-01T10:02:00Z"}`)
	once, _ := UnionJSONL(local, remote)
	twice, _ := UnionJSONL(once, remote)
	thrice, _ := UnionJSONL(twice, once)
	if string(once) != string(twice) || string(twice) != string(thrice) {
		t.Fatalf("not idempotent:\n1: %q\n2: %q\n3: %q", once, twice, thrice)
	}
}

func TestUnionCorruptLineNeverDropped(t *testing.T) {
	local := []byte(`{"uuid":"A","parentUuid":null,"timestamp":"2026-07-01T10:00:00Z"}
{this is a truncated broken line`)
	remote := []byte(`{"uuid":"A","parentUuid":null,"timestamp":"2026-07-01T10:00:00Z"}`)
	out, err := UnionJSONL(local, remote)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "truncated broken line") {
		t.Fatalf("corrupt line was dropped: %q", out)
	}
}

func TestUnionKeylessSummaryLine(t *testing.T) {
	local := []byte(`{"type":"summary","summary":"did stuff","leafUuid":"Z"}
{"uuid":"A","parentUuid":null,"timestamp":"2026-07-01T10:00:00Z"}`)
	remote := []byte(`{"type":"summary","summary":"did stuff","leafUuid":"Z"}
{"uuid":"A","parentUuid":null,"timestamp":"2026-07-01T10:00:00Z"}`)
	out, err := UnionJSONL(local, remote)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(out), "did stuff"); n != 1 {
		t.Fatalf("summary duplicated %d times", n)
	}
}
