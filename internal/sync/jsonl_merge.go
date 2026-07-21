package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// maxMergeLineBytes bounds how much of a single JSONL line we parse. Session
// files carry multi-megabyte tool results on their own lines; anything larger
// is keyed by a raw content hash rather than parsed, but is still preserved.
const maxMergeLineBytes = 8 * 1024 * 1024

type jsonlNode struct {
	raw    string
	uuid   string
	parent string
	ts     time.Time
	idx    int
}

type recordHeader struct {
	UUID       string `json:"uuid"`
	ParentUUID string `json:"parentUuid"`
	Timestamp  string `json:"timestamp"`
}

// contentHashKey keys a line that has no uuid (summary/meta lines) or cannot be
// parsed (corrupt/partial). JSON objects are canonicalised via re-marshal so
// key order and whitespace never produce a false difference.
func contentHashKey(line []byte) string {
	var m map[string]interface{}
	if err := json.Unmarshal(line, &m); err == nil {
		if canon, err := json.Marshal(m); err == nil { // Go sorts map keys
			sum := sha256.Sum256(canon)
			return "h:" + hex.EncodeToString(sum[:])
		}
	}
	sum := sha256.Sum256(line)
	return "raw:" + hex.EncodeToString(sum[:])
}

func parseNode(line string, idx int) (jsonlNode, string) {
	n := jsonlNode{raw: line, idx: idx}
	b := []byte(line)
	if len(b) > maxMergeLineBytes {
		return n, contentHashKey(b)
	}
	var h recordHeader
	if err := json.Unmarshal(b, &h); err != nil {
		return n, contentHashKey(b) // corrupt/partial: keyed by raw, never dropped
	}
	if h.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339Nano, h.Timestamp); err == nil {
			n.ts = t
		}
	}
	if h.UUID != "" {
		n.uuid = h.UUID
		n.parent = h.ParentUUID
		return n, h.UUID
	}
	return n, contentHashKey(b)
}

func keySet(data []byte) map[string]struct{} {
	set := make(map[string]struct{})
	for _, raw := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		_, k := parseNode(raw, 0)
		set[k] = struct{}{}
	}
	return set
}

// UnionJSONL merges two append-only session logs into their set-union, keyed by
// record uuid (falling back to a canonical content hash for uuid-less or
// unparseable lines). Output is topologically ordered so a parent always
// precedes its children; ties break by (timestamp, original index), which
// tolerates clock skew between machines. The operation is idempotent and never
// drops a line. Both inputs must be in the same (portable) path space.
func UnionJSONL(local, remote []byte) ([]byte, error) {
	localUnique := len(keySet(local))
	remoteUnique := len(keySet(remote))

	nodes := make(map[string]jsonlNode)
	idx := 0
	for _, data := range [][]byte{local, remote} {
		for _, raw := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			n, key := parseNode(raw, idx)
			idx++
			if _, seen := nodes[key]; !seen {
				nodes[key] = n // first occurrence wins (local before remote)
			}
		}
	}

	uuidSet := make(map[string]struct{})
	for _, n := range nodes {
		if n.uuid != "" {
			uuidSet[n.uuid] = struct{}{}
		}
	}
	children := make(map[string][]string)
	indeg := make(map[string]int)
	for key, n := range nodes {
		if n.uuid != "" && n.parent != "" {
			if _, ok := uuidSet[n.parent]; ok {
				children[n.parent] = append(children[n.parent], key)
				indeg[key] = 1
				continue
			}
		}
		indeg[key] = 0
	}

	less := func(a, b string) bool {
		na, nb := nodes[a], nodes[b]
		if !na.ts.Equal(nb.ts) {
			return na.ts.Before(nb.ts)
		}
		return na.idx < nb.idx
	}

	var ready []string
	for key := range nodes {
		if indeg[key] == 0 {
			ready = append(ready, key)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return less(ready[i], ready[j]) })

	out := make([]string, 0, len(nodes))
	for len(ready) > 0 {
		key := ready[0]
		ready = ready[1:]
		out = append(out, nodes[key].raw)
		kids := children[nodes[key].uuid]
		sort.Slice(kids, func(i, j int) bool { return less(kids[i], kids[j]) })
		for _, c := range kids {
			indeg[c]--
			if indeg[c] == 0 {
				pos := sort.Search(len(ready), func(i int) bool { return less(c, ready[i]) })
				ready = append(ready, "")
				copy(ready[pos+1:], ready[pos:])
				ready[pos] = c
			}
		}
	}

	// Cycle safety net (unexpected): emit every node once, deterministically.
	if len(out) < len(nodes) {
		keys := make([]string, 0, len(nodes))
		for k := range nodes {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return less(keys[i], keys[j]) })
		out = out[:0]
		for _, k := range keys {
			out = append(out, nodes[k].raw)
		}
	}

	if len(out) < localUnique || len(out) < remoteUnique {
		return nil, fmt.Errorf("union produced %d records, fewer than inputs (local=%d remote=%d); refusing merge", len(out), localUnique, remoteUnique)
	}
	return []byte(strings.Join(out, "\n") + "\n"), nil
}

// isSessionJSONL reports whether a relative path is a mergeable session log:
// a *.jsonl under projects/ that is not itself a conflict copy.
func isSessionJSONL(relPath string) bool {
	if strings.Contains(relPath, ".conflict.") {
		return false
	}
	return strings.HasPrefix(relPath, "projects/") && strings.HasSuffix(relPath, ".jsonl")
}
