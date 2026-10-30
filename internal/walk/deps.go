package walk

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// Dependencies between the changed files, read from the lines a file adds:
// when a.ts now imports ./b, b is read first. Rough on purpose — it only
// nudges the order; the layers and the AI do the rest.

var (
	importLine = regexp.MustCompile(`^\s*(import\b|from\s+\S+\s+import\b|export\s.*\bfrom\b|const\s.*=\s*require\(|.*\brequire\(|use\s|#include\b|using\s|@import\b|require_relative\b|.*\bimport\(|package\s)`)
	quoted     = regexp.MustCompile(`["'` + "`" + `]([^"'` + "`" + `\s]+)["'` + "`" + `]`)
	pyFrom     = regexp.MustCompile(`^\s*from\s+([A-Za-z0-9_.]+)\s+import`)
	pyImport   = regexp.MustCompile(`^\s*import\s+([A-Za-z0-9_.]+)`)
	codeExt    = regexp.MustCompile(`\.(tsx?|jsx?|mjs|cjs|vue|svelte|py|go|rb|rs|java|kt|cs|php|dart|swift|scss|css)$`)
)

// Edge says From uses To (so To is read first).
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Dependencies finds the edges among the changed files.
func Dependencies(files []File) []Edge {
	byStem := map[string][]string{} // path without extension → paths
	byDir := map[string][]string{}  // directory → code paths (Go packages, Python modules)
	for _, f := range files {
		if f.Status == "removed" || !codeExt.MatchString(f.Path) {
			continue
		}
		stem := strings.TrimSuffix(f.Path, path.Ext(f.Path))
		byStem[stem] = append(byStem[stem], f.Path)
		if strings.HasSuffix(stem, "/index") {
			byStem[strings.TrimSuffix(stem, "/index")] = append(byStem[strings.TrimSuffix(stem, "/index")], f.Path)
		}
		byDir[path.Dir(f.Path)] = append(byDir[path.Dir(f.Path)], f.Path)
	}
	seen := map[Edge]bool{}
	var out []Edge
	add := func(from, to string) {
		e := Edge{from, to}
		if from != to && !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	for _, f := range files {
		if f.Status == "removed" {
			continue
		}
		dir := path.Dir(f.Path)
		goFile := strings.HasSuffix(f.Path, ".go")
		for _, l := range AddedText(f.Patch) {
			if !importLine.MatchString(l) && !(goFile && quoted.MatchString(l) && strings.HasPrefix(strings.TrimSpace(l), `"`)) {
				continue
			}
			var specs []string
			for _, m := range quoted.FindAllStringSubmatch(l, -1) {
				specs = append(specs, m[1])
			}
			if m := pyFrom.FindStringSubmatch(l); m != nil {
				specs = append(specs, "py:"+m[1])
			} else if m := pyImport.FindStringSubmatch(l); m != nil && strings.HasSuffix(f.Path, ".py") {
				specs = append(specs, "py:"+m[1])
			}
			for _, spec := range specs {
				switch {
				case strings.HasPrefix(spec, "py:"):
					mod := strings.ReplaceAll(strings.TrimLeft(strings.TrimPrefix(spec, "py:"), "."), ".", "/")
					for stem, ps := range byStem {
						if mod != "" && (stem == mod || strings.HasSuffix(stem, "/"+mod) || strings.HasSuffix(stem, "/"+mod+"/__init__")) {
							for _, p := range ps {
								add(f.Path, p)
							}
						}
					}
				case strings.HasPrefix(spec, "."):
					target := path.Clean(path.Join(dir, spec))
					target = strings.TrimSuffix(target, path.Ext(target))
					for _, p := range byStem[target] {
						add(f.Path, p)
					}
				case goFile:
					// a Go import path ends in the package's directory
					for d, ps := range byDir {
						if d != "." && d != dir && (strings.HasSuffix(spec, "/"+d) || spec == d) {
							for _, p := range ps {
								if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
									add(f.Path, p)
								}
							}
						}
					}
				default:
					// aliases like @/lib/x or src/lib/x: match on the tail
					tail := strings.TrimPrefix(strings.TrimPrefix(spec, "@/"), "~/")
					tail = strings.TrimSuffix(tail, path.Ext(tail))
					if strings.Count(tail, "/") < 1 {
						continue
					}
					for stem, ps := range byStem {
						if strings.HasSuffix(stem, "/"+tail) || stem == tail {
							for _, p := range ps {
								add(f.Path, p)
							}
						}
					}
				}
			}
		}
	}
	// a test reads after the file it tests, even without an import
	for _, f := range files {
		base := path.Base(f.Path)
		for _, suf := range []string{"_test.go", ".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx", ".test.js", ".spec.js", "_test.py"} {
			if strings.HasSuffix(base, suf) {
				orig := strings.TrimSuffix(f.Path, suf)
				for stem, ps := range byStem {
					if stem == orig {
						for _, p := range ps {
							add(f.Path, p)
						}
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// Order sorts paths by layer rank, keeping every file after the files it
// uses (Kahn's algorithm; the lowest rank goes first, a cycle is broken at
// its lowest-ranked file).
func Order(paths []string, rank func(string) int, edges []Edge) []string {
	in := map[string]map[string]bool{} // p → files it waits for
	out := map[string][]string{}
	known := map[string]bool{}
	for _, p := range paths {
		known[p] = true
		in[p] = map[string]bool{}
	}
	for _, e := range edges {
		if !known[e.From] || !known[e.To] {
			continue
		}
		// a file never waits for a later layer: tests and docs do not pull the UI forward
		if rank(e.To) > rank(e.From) {
			continue
		}
		in[e.From][e.To] = true
		out[e.To] = append(out[e.To], e.From)
	}
	less := func(a, b string) bool {
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra < rb
		}
		return a < b
	}
	done := map[string]bool{}
	var res []string
	for len(res) < len(paths) {
		best := ""
		for _, p := range paths {
			if done[p] || len(in[p]) > 0 {
				continue
			}
			if best == "" || less(p, best) {
				best = p
			}
		}
		if best == "" { // a cycle: take the lowest-ranked waiting file
			for _, p := range paths {
				if !done[p] && (best == "" || less(p, best)) {
					best = p
				}
			}
		}
		done[best] = true
		res = append(res, best)
		for _, q := range out[best] {
			delete(in[q], best)
		}
	}
	return res
}
