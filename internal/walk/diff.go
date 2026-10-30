// Package walk turns a pull request's diff into a walkthrough: the changes
// in the order a reviewer should read them, grouped into steps, with one
// sentence per file — and checks that every changed line is in it once.
package walk

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

// Line is one line of a hunk: T is '+', '-', ' ' or '\\' (no newline at end).
type Line struct {
	T string `json:"t"`
	O int    `json:"o,omitempty"` // line number in the old file
	N int    `json:"n,omitempty"` // line number in the new file
	S string `json:"s"`
}

// Hunk is one "@@" block of a unified diff.
type Hunk struct {
	Header   string `json:"header"`
	OldStart int    `json:"old_start"`
	OldLines int    `json:"old_lines"`
	NewStart int    `json:"new_start"`
	NewLines int    `json:"new_lines"`
	Lines    []Line `json:"lines"`
	Hash     string `json:"hash"`
}

var hunkHead = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@ ?(.*)$`)

// ParsePatch reads GitHub's per-file patch (hunks only, no file header).
func ParsePatch(patch string) []Hunk {
	var out []Hunk
	var cur *Hunk
	o, n := 0, 0
	flush := func() {
		if cur != nil {
			cur.Hash = hashLines(cur.Lines)
			out = append(out, *cur)
			cur = nil
		}
	}
	for _, raw := range strings.Split(patch, "\n") {
		if m := hunkHead.FindStringSubmatch(raw); m != nil {
			flush()
			cur = &Hunk{Header: strings.TrimSpace(m[5])}
			cur.OldStart, _ = strconv.Atoi(m[1])
			cur.OldLines = 1
			if m[2] != "" {
				cur.OldLines, _ = strconv.Atoi(m[2])
			}
			cur.NewStart, _ = strconv.Atoi(m[3])
			cur.NewLines = 1
			if m[4] != "" {
				cur.NewLines, _ = strconv.Atoi(m[4])
			}
			o, n = cur.OldStart, cur.NewStart
			continue
		}
		if cur == nil {
			continue
		}
		if raw == "" {
			// a trailing empty string after the last newline, or an empty context line
			continue
		}
		switch raw[0] {
		case '+':
			cur.Lines = append(cur.Lines, Line{T: "+", N: n, S: raw[1:]})
			n++
		case '-':
			cur.Lines = append(cur.Lines, Line{T: "-", O: o, S: raw[1:]})
			o++
		case '\\':
			cur.Lines = append(cur.Lines, Line{T: "\\", S: strings.TrimSpace(raw[1:])})
		default:
			s := raw
			if s[0] == ' ' {
				s = s[1:]
			}
			cur.Lines = append(cur.Lines, Line{T: " ", O: o, N: n, S: s})
			o++
			n++
		}
	}
	flush()
	return out
}

// hashLines identifies a hunk by what it changes, not where: a later commit
// that only moves it keeps the hash (and so "viewed"), one that edits it does not.
func hashLines(lines []Line) string {
	h := sha256.New()
	for _, l := range lines {
		if l.T == "+" || l.T == "-" {
			h.Write([]byte(l.T))
			h.Write([]byte(strings.TrimRight(l.S, " \t\r")))
			h.Write([]byte{'\n'})
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// FileHash stands in for a file without hunks (binary, renamed only, too large).
func FileHash(path, status string, add, del int, patch string) string {
	h := sha256.Sum256([]byte(path + "\x00" + status + "\x00" + strconv.Itoa(add) + "/" + strconv.Itoa(del) + "\x00" + patch))
	return "f" + hex.EncodeToString(h[:])[:15]
}

// Hashes are the identities of a file's hunks (or the one stand-in).
func Hashes(f File) []string {
	hs := ParsePatch(f.Patch)
	if len(hs) == 0 {
		return []string{FileHash(f.Path, f.Status, f.Additions, f.Deletions, f.Patch)}
	}
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.Hash
	}
	return out
}

// AddedText is the added lines of a patch (what dependency hints read).
func AddedText(patch string) []string {
	var out []string
	for _, l := range strings.Split(patch, "\n") {
		if strings.HasPrefix(l, "+") {
			out = append(out, l[1:])
		}
	}
	return out
}
