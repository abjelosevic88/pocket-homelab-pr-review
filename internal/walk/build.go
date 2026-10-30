package walk

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// File is one changed file going into a walkthrough.
type File struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"-"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Layer     string `json:"layer"`
	Summary   string `json:"summary"`
}

// Commit is one commit as the story of how it was built knows it.
type Commit struct {
	SHA     string
	Message string
	Paths   []string // the files it touched, in GitHub's order
}

// Input is everything a walkthrough is made from.
type Input struct {
	Repo, Title, Body, BaseRef, HeadRef string
	Files                               []File
	Commits                             []Commit
	AgentOrder                          []string // files in the order an agent edited them, when known
	Config                              *RepoConfig
	Hints                               string // the person's own
	Language                            string
}

// AI is what writes the sentences: Pocket AI or an OpenAI-compatible API.
type AI interface {
	Complete(ctx context.Context, system, user string, json bool) (string, error)
}

// Doc is a finished walkthrough.
type Doc struct {
	Title     string   `json:"title"`
	Overview  string   `json:"overview"`
	Before    string   `json:"before"`
	After     string   `json:"after"`
	Steps     []Step   `json:"steps"`
	Noise     []Unit   `json:"noise"`
	Risks     []Risk   `json:"risks"`
	Questions []string `json:"questions"`
	Order     string   `json:"order"` // ai | layers: who decided the order
	Warnings  []string `json:"warnings"`
	Stats     Stats    `json:"stats"`
	Edges     []Edge   `json:"edges,omitempty"`
}

// Step is one part of the story.
type Step struct {
	Key   string   `json:"key"`
	Title string   `json:"title"`
	Why   string   `json:"why"`
	Layer string   `json:"layer"`
	Risks []string `json:"risks"`
	Units []Unit   `json:"units"`
}

// Unit is a file, or some hunks of one, as a step shows it.
type Unit struct {
	ID      string   `json:"id"`
	Path    string   `json:"path"`
	Hunks   []int    `json:"hunks"` // nil = the whole file
	Hashes  []string `json:"hashes"`
	Summary string   `json:"summary"`
	Layer   string   `json:"layer"`
}

// Risk is something worth a careful look.
type Risk struct {
	Level string   `json:"level"` // high | medium | low
	Text  string   `json:"text"`
	Units []string `json:"units"`
}

// Stats count what is in it.
type Stats struct {
	Steps     int `json:"steps"`
	Units     int `json:"units"`
	Files     int `json:"files"`
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
}

// Limits for the AI calls (Pocket AI takes at most 512 KB a call).
const (
	batchChars   = 60000
	filePatchCap = 9000
	maxBatches   = 40
	maxPlanUnits = 700
	parallel     = 3
)

// Build makes the walkthrough. progress gets short status lines. With ai nil
// (or when the AI fails) the order comes from the layers and the imports.
func Build(ctx context.Context, in Input, ai AI, progress func(string)) (*Doc, []File) {
	if progress == nil {
		progress = func(string) {}
	}
	files := append([]File(nil), in.Files...)
	for i := range files {
		if files[i].Layer == "" {
			files[i].Layer = in.Config.Classify(files[i].Path, files[i].Patch)
		}
	}
	doc := &Doc{Title: in.Title, Order: "layers", Warnings: []string{}, Risks: []Risk{}, Questions: []string{}, Noise: []Unit{}}
	edges := Dependencies(files)
	doc.Edges = edges

	// units: one per file until the AI splits a file into parts
	byPath := map[string]*File{}
	for i := range files {
		byPath[files[i].Path] = &files[i]
	}
	parts := map[string][][]int{} // path → hunk groups (from the AI)
	sums := map[string][]string{} // path → summary per part
	if ai != nil {
		summarise(ctx, in, files, ai, parts, sums, doc, progress)
	}
	for i := range files {
		if s := sums[files[i].Path]; len(s) > 0 && files[i].Summary == "" {
			files[i].Summary = strings.Join(nonEmpty(s), " ")
		}
		if files[i].Summary == "" {
			files[i].Summary = plainSummary(files[i])
		}
	}

	rank := func(p string) int {
		if f := byPath[p]; f != nil {
			return in.Config.Rank(f.Layer)
		}
		return 2000
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	ordered := Order(paths, rank, edges)

	var units []Unit
	var noise []Unit
	n := 0
	for _, p := range ordered {
		f := byPath[p]
		hashes := Hashes(*f)
		groups := parts[p]
		if len(groups) <= 1 || len(hashes) <= 1 {
			groups = [][]int{nil}
		}
		for gi, g := range groups {
			n++
			u := Unit{ID: fmt.Sprintf("u%d", n), Path: p, Layer: f.Layer, Hunks: g}
			if g == nil {
				u.Hashes = hashes
				u.Summary = f.Summary
			} else {
				for _, h := range g {
					if h >= 0 && h < len(hashes) {
						u.Hashes = append(u.Hashes, hashes[h])
					}
				}
				if s := sums[p]; gi < len(s) {
					u.Summary = s[gi]
				}
				if u.Summary == "" {
					u.Summary = f.Summary
				}
			}
			if f.Layer == "noise" {
				noise = append(noise, u)
			} else {
				units = append(units, u)
			}
		}
	}
	if noise != nil {
		doc.Noise = noise
	}

	planned := false
	if ai != nil && len(units) > 0 {
		if len(units) > maxPlanUnits {
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("This pull request has %d parts, too many for the AI to order at once; they are ordered by layer.", len(units)))
		} else {
			progress("Putting the changes in order")
			if err := plan(ctx, in, units, edges, ai, doc); err != nil {
				doc.Warnings = append(doc.Warnings, "The AI could not put it in order ("+short(err.Error())+"); this is the order by layer.")
			} else {
				planned = true
				doc.Order = "ai"
			}
		}
	}
	if !planned {
		doc.Steps = layerSteps(in.Config, units)
		if doc.Overview == "" {
			doc.Overview = firstParagraph(in.Body)
		}
	}
	for i := range doc.Steps {
		doc.Steps[i].Key = stepKey(doc.Steps[i], i)
	}
	doc.Stats = Stats{Steps: len(doc.Steps), Files: len(files)}
	for _, st := range doc.Steps {
		doc.Stats.Units += len(st.Units)
	}
	for _, f := range files {
		doc.Stats.Additions += f.Additions
		doc.Stats.Deletions += f.Deletions
	}
	var missing []string
	for _, f := range files {
		if f.Truncated {
			missing = append(missing, f.Path)
		}
	}
	if len(missing) > 0 {
		doc.Warnings = append(doc.Warnings, fmt.Sprintf("GitHub sent no diff for %d large file(s): %s. Open them on GitHub.", len(missing), strings.Join(clipList(missing, 4), ", ")))
	}
	return doc, files
}

// FileOrder is each file's place in reading order (noise last).
func (d *Doc) FileOrder() map[string]int {
	out := map[string]int{}
	add := func(p string) {
		if _, ok := out[p]; !ok {
			out[p] = len(out)
		}
	}
	for _, s := range d.Steps {
		for _, u := range s.Units {
			add(u.Path)
		}
	}
	for _, u := range d.Noise {
		add(u.Path)
	}
	return out
}

// ---- the AI, part 1: one sentence per file ----

const summaryVoice = `You explain a pull request to the person who has to review it. For every file below, write one plain sentence (at most 25 words) saying what the change in that file does — and why, when the diff shows it.
Name the concrete things: tables, columns, functions, routes, settings, screens. Do not start with "This file" or "Changes". No marketing words.
If the hunks of a file do clearly different jobs that a reviewer would read at different moments (for example one hunk adds a route, another fixes unrelated logging), split them into parts: each part lists its hunk numbers and has its own sentence. Most files have exactly one part; then leave parts out.
Answer with JSON only: {"files":[{"path":"…","summary":"…","parts":[{"hunks":[0,1],"summary":"…"}]}]}`

func summarise(ctx context.Context, in Input, files []File, ai AI, parts map[string][][]int, sums map[string][]string, doc *Doc, progress func(string)) {
	var batches [][]int
	var cur []int
	size := 0
	for i, f := range files {
		if f.Layer == "noise" {
			continue
		}
		t := len(clipPatch(f.Patch, filePatchCap)) + len(f.Path) + 80
		if len(cur) > 0 && (size+t > batchChars || len(cur) >= 40) {
			batches = append(batches, cur)
			cur, size = nil, 0
		}
		cur = append(cur, i)
		size += t
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	if len(batches) > maxBatches {
		doc.Warnings = append(doc.Warnings, fmt.Sprintf("Only the first %d groups of files got a sentence from the AI; the pull request is very large.", maxBatches))
		batches = batches[:maxBatches]
	}
	head := "Pull request: " + in.Title + "\n"
	if b := strings.TrimSpace(in.Body); b != "" {
		head += "Description:\n" + clipText(b, 3000) + "\n"
	}
	if h := strings.TrimSpace(hintsOf(in)); h != "" {
		head += "Notes on this codebase:\n" + clipText(h, 2000) + "\n"
	}
	system := summaryVoice + languageLine(in.Language)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)
	done, failed := 0, 0
	var firstErr string
	progress(fmt.Sprintf("Reading the files (0 of %d)", len(files)))
	for _, b := range batches {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx []int) {
			defer wg.Done()
			defer func() { <-sem }()
			var sb strings.Builder
			sb.WriteString(head)
			sb.WriteString("\nFiles:\n")
			for _, i := range idx {
				f := files[i]
				fmt.Fprintf(&sb, "\n### %s (%s, +%d -%d)\n", f.Path, statusWord(f), f.Additions, f.Deletions)
				if f.Binary || f.Patch == "" {
					sb.WriteString("(no text diff)\n")
					continue
				}
				for hi, h := range ParsePatch(clipPatch(f.Patch, filePatchCap)) {
					fmt.Fprintf(&sb, "[hunk %d] @@ -%d,%d +%d,%d @@ %s\n", hi, h.OldStart, h.OldLines, h.NewStart, h.NewLines, h.Header)
					for _, l := range h.Lines {
						sb.WriteString(l.T)
						sb.WriteString(l.S)
						sb.WriteByte('\n')
					}
				}
			}
			out, err := ai.Complete(ctx, system, sb.String(), true)
			var res struct {
				Files []struct {
					Path    string `json:"path"`
					Summary string `json:"summary"`
					Parts   []struct {
						Hunks   []int  `json:"hunks"`
						Summary string `json:"summary"`
					} `json:"parts"`
				} `json:"files"`
			}
			if err == nil {
				err = unmarshalLoose(out, &res)
			}
			mu.Lock()
			defer mu.Unlock()
			done += len(idx)
			progress(fmt.Sprintf("Reading the files (%d of %d)", done, len(files)))
			if err != nil {
				failed++
				if firstErr == "" {
					firstErr = err.Error()
				}
				return
			}
			want := map[string]bool{}
			for _, i := range idx {
				want[files[i].Path] = true
			}
			for _, r := range res.Files {
				if !want[r.Path] {
					continue
				}
				if len(r.Parts) > 1 {
					var groups [][]int
					var texts []string
					for _, p := range r.Parts {
						groups = append(groups, p.Hunks)
						texts = append(texts, oneLine(p.Summary, 300))
					}
					if g := validGroups(groups, len(Hashes(*fileByPath(files, r.Path)))); g != nil {
						parts[r.Path] = g
						sums[r.Path] = texts
						continue
					}
				}
				sums[r.Path] = []string{oneLine(r.Summary, 300)}
			}
		}(b)
	}
	wg.Wait()
	if failed > 0 {
		doc.Warnings = append(doc.Warnings, fmt.Sprintf("The AI did not answer for %d of %d groups of files (%s); those files have a plain description.", failed, len(batches), short(firstErr)))
	}
}

// validGroups keeps an AI split only when it covers every hunk exactly once.
func validGroups(groups [][]int, hunks int) [][]int {
	seen := make([]bool, hunks)
	var out [][]int
	for _, g := range groups {
		var keep []int
		for _, h := range g {
			if h < 0 || h >= hunks || seen[h] {
				return nil
			}
			seen[h] = true
			keep = append(keep, h)
		}
		if len(keep) > 0 {
			sort.Ints(keep)
			out = append(out, keep)
		}
	}
	for _, s := range seen {
		if !s {
			return nil
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

func fileByPath(files []File, p string) *File {
	for i := range files {
		if files[i].Path == p {
			return &files[i]
		}
	}
	return &File{}
}

// ---- the AI, part 2: the order ----

const planVoice = `You turn a pull request into a guided walkthrough for its reviewer. Below are its changes as numbered units (a file, or a part of one) with one sentence each, in a first rough order by layer and imports.
Arrange them into the order a reviewer should read them — from the foundation up: the data and schema first, then the code that reads and writes it, the logic built on it, the API or routes that expose it, the configuration, prompts and integrations that use it, then the screens; each test next to or right after what it tests. Follow the story of the change, not the alphabet.
Group the units that belong to one idea into one step: usually 3 to 9 steps, a tiny pull request may have 1 or 2. For each step write:
- title: short, naming the concrete thing ("Migration for the coupons table", "Routes to create and redeem a coupon")
- why: one or two sentences — what this step does in the whole change and what to check
- units: the unit ids in reading order
- risks: concrete things in this step worth a careful look (empty when there are none); never generic advice
Also write:
- overview: the pull request in one minute — 3 to 5 sentences: the problem, the approach, what changes for the people using it
- before / after: how it behaves before and after, one or two sentences each
- risks: the most important concerns across the pull request, each with level high, medium or low and the unit ids it is about
- questions: up to 5 things the reviewer should ask the author
Every unit id must appear in exactly one step. Do not invent changes that are not in the units.
Answer with JSON only: {"overview":"…","before":"…","after":"…","steps":[{"title":"…","why":"…","units":["u1"],"risks":["…"]}],"risks":[{"level":"medium","text":"…","units":["u3"]}],"questions":["…"]}`

func plan(ctx context.Context, in Input, units []Unit, edges []Edge, ai AI, doc *Doc) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Pull request: %s\nBranch: %s → %s\n", in.Title, in.HeadRef, in.BaseRef)
	if b := strings.TrimSpace(in.Body); b != "" {
		sb.WriteString("Description:\n" + clipText(b, 4000) + "\n")
	}
	if len(in.Commits) > 0 {
		sb.WriteString("\nCommits, oldest first:\n")
		for i, c := range in.Commits {
			if i >= 60 {
				fmt.Fprintf(&sb, "… and %d more\n", len(in.Commits)-60)
				break
			}
			fmt.Fprintf(&sb, "- %s\n", oneLine(c.Message, 160))
		}
	}
	if len(in.AgentOrder) > 0 {
		sb.WriteString("\nThe order the coding agent edited the files in:\n")
		for i, p := range in.AgentOrder {
			if i >= 120 {
				break
			}
			fmt.Fprintf(&sb, "%d. %s\n", i+1, p)
		}
	}
	if h := strings.TrimSpace(hintsOf(in)); h != "" {
		sb.WriteString("\nNotes on this codebase (follow them):\n" + clipText(h, 3000) + "\n")
	}
	sb.WriteString("\nUnits (id | path | layer | change | what it does):\n")
	id := map[string]string{} // path → first unit id
	for _, u := range units {
		f := u.Path
		if _, ok := id[f]; !ok {
			id[f] = u.ID
		}
		fmt.Fprintf(&sb, "%s | %s | %s | %s\n", u.ID, u.Path, in.Config.LayerName(u.Layer), oneLine(u.Summary, 240))
	}
	if len(edges) > 0 {
		sb.WriteString("\nWhich unit uses which (read the used one first):\n")
		for i, e := range edges {
			if i >= 300 {
				break
			}
			if a, b := id[e.From], id[e.To]; a != "" && b != "" {
				fmt.Fprintf(&sb, "%s uses %s\n", a, b)
			}
		}
	}
	out, err := ai.Complete(ctx, planVoice+languageLine(in.Language), sb.String(), true)
	if err != nil {
		return err
	}
	var res struct {
		Overview string `json:"overview"`
		Before   string `json:"before"`
		After    string `json:"after"`
		Steps    []struct {
			Title string   `json:"title"`
			Why   string   `json:"why"`
			Units []string `json:"units"`
			Risks []string `json:"risks"`
		} `json:"steps"`
		Risks []struct {
			Level string   `json:"level"`
			Text  string   `json:"text"`
			Units []string `json:"units"`
		} `json:"risks"`
		Questions []string `json:"questions"`
	}
	if err := unmarshalLoose(out, &res); err != nil {
		return err
	}
	if len(res.Steps) == 0 {
		return fmt.Errorf("no steps in the answer")
	}
	byID := map[string]Unit{}
	for _, u := range units {
		byID[u.ID] = u
	}
	// coverage: every unit exactly once; unknown ids dropped, missing ones
	// go to the step that already has that file, else to "Everything else"
	used := map[string]bool{}
	var steps []Step
	for _, s := range res.Steps {
		st := Step{Title: oneLine(s.Title, 120), Why: oneLine(s.Why, 600), Risks: []string{}}
		for _, id := range s.Units {
			id = strings.TrimSpace(id)
			u, ok := byID[id]
			if !ok || used[id] {
				continue
			}
			used[id] = true
			st.Units = append(st.Units, u)
		}
		for _, r := range s.Risks {
			if r = oneLine(r, 400); r != "" {
				st.Risks = append(st.Risks, r)
			}
		}
		if len(st.Units) == 0 {
			continue
		}
		if st.Title == "" {
			st.Title = in.Config.LayerName(st.Units[0].Layer)
		}
		st.Layer = st.Units[0].Layer
		steps = append(steps, st)
	}
	var left []Unit
	for _, u := range units {
		if used[u.ID] {
			continue
		}
		placed := false
		for i := range steps {
			for _, x := range steps[i].Units {
				if x.Path == u.Path {
					steps[i].Units = append(steps[i].Units, u)
					placed = true
					break
				}
			}
			if placed {
				break
			}
		}
		if !placed {
			left = append(left, u)
		}
	}
	if len(left) > 0 {
		steps = append(steps, Step{Title: "Everything else", Why: "Changes the walkthrough did not place in a step above.", Layer: left[0].Layer, Risks: []string{}, Units: left})
		doc.Warnings = append(doc.Warnings, fmt.Sprintf("%d part(s) the AI left out are under “Everything else”.", len(left)))
	}
	if len(steps) == 0 {
		return fmt.Errorf("the steps had no known units")
	}
	doc.Steps = steps
	doc.Overview = strings.TrimSpace(res.Overview)
	doc.Before = strings.TrimSpace(res.Before)
	doc.After = strings.TrimSpace(res.After)
	for _, r := range res.Risks {
		lvl := strings.ToLower(strings.TrimSpace(r.Level))
		if lvl != "high" && lvl != "medium" && lvl != "low" {
			lvl = "medium"
		}
		ids := []string{}
		for _, id := range r.Units {
			if _, ok := byID[id]; ok {
				ids = append(ids, id)
			}
		}
		if t := oneLine(r.Text, 600); t != "" {
			doc.Risks = append(doc.Risks, Risk{Level: lvl, Text: t, Units: ids})
		}
	}
	for _, q := range res.Questions {
		if q = oneLine(q, 400); q != "" && len(doc.Questions) < 8 {
			doc.Questions = append(doc.Questions, q)
		}
	}
	return nil
}

// ---- without the AI ----

// layerSteps makes one step per layer, in reading order.
func layerSteps(cfg *RepoConfig, units []Unit) []Step {
	var steps []Step
	idx := map[string]int{}
	for _, u := range units {
		i, ok := idx[u.Layer]
		if !ok {
			i = len(steps)
			idx[u.Layer] = i
			steps = append(steps, Step{Title: cfg.LayerName(u.Layer), Layer: u.Layer, Why: layerWhy(u.Layer), Risks: []string{}})
		}
		steps[i].Units = append(steps[i].Units, u)
	}
	sort.SliceStable(steps, func(a, b int) bool { return cfg.Rank(steps[a].Layer) < cfg.Rank(steps[b].Layer) })
	return steps
}

func layerWhy(layer string) string {
	switch layer {
	case "schema":
		return "What is stored changes here: check names, types, defaults, indexes and that the migration can run on existing data."
	case "model":
		return "The shapes everything else uses."
	case "data":
		return "How the data is read and written: check the queries, transactions and who may see what."
	case "logic":
		return "The behaviour built on the data."
	case "api":
		return "How the rest of the world reaches it: check input validation, permissions and error answers."
	case "config":
		return "Settings, prompts and integrations that use the new pieces."
	case "ui":
		return "What people see and click."
	case "tests":
		return "What is proven to work — and what is not tested."
	case "docs":
		return "What is written down for others."
	case "build":
		return "Dependencies, build and CI."
	}
	return ""
}

func plainSummary(f File) string {
	switch {
	case f.Status == "added":
		return fmt.Sprintf("New file, %d lines.", f.Additions)
	case f.Status == "removed":
		return fmt.Sprintf("Removed (%d lines).", f.Deletions)
	case f.Status == "renamed" && f.Additions+f.Deletions == 0:
		return "Renamed from " + f.OldPath + ", no other change."
	case f.Status == "renamed":
		return fmt.Sprintf("Renamed from %s; %d lines added, %d removed.", f.OldPath, f.Additions, f.Deletions)
	case f.Binary:
		return "Binary file changed."
	}
	return fmt.Sprintf("%d lines added, %d removed.", f.Additions, f.Deletions)
}

func statusWord(f File) string {
	if f.Status == "renamed" && f.OldPath != "" {
		return "renamed from " + f.OldPath
	}
	return f.Status
}

func stepKey(s Step, i int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s.Title) {
		if b.Len() >= 40 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	return fmt.Sprintf("%d-%s", i+1, strings.Trim(b.String(), "-"))
}

func hintsOf(in Input) string {
	var parts []string
	if in.Config != nil && strings.TrimSpace(in.Config.Hints) != "" {
		parts = append(parts, strings.TrimSpace(in.Config.Hints))
	}
	if strings.TrimSpace(in.Hints) != "" {
		parts = append(parts, strings.TrimSpace(in.Hints))
	}
	return strings.Join(parts, "\n")
}

func languageLine(lang string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" || strings.EqualFold(lang, "english") {
		return "\nWrite in plain English."
	}
	return "\nWrite every sentence in " + lang + " (keep code names as they are)."
}

// ---- small helpers ----

func unmarshalLoose(text string, v any) error {
	t := strings.TrimSpace(text)
	t = strings.TrimPrefix(t, "```json")
	t = strings.TrimPrefix(t, "```")
	t = strings.TrimSuffix(t, "```")
	if i, j := strings.Index(t, "{"), strings.LastIndex(t, "}"); i >= 0 && j > i {
		t = t[i : j+1]
	}
	if err := json.Unmarshal([]byte(t), v); err != nil {
		return fmt.Errorf("the AI did not answer in the expected form")
	}
	return nil
}

func clipPatch(p string, n int) string {
	if len(p) <= n {
		return p
	}
	cut := strings.LastIndex(p[:n], "\n")
	if cut < 0 {
		cut = n
	}
	rest := strings.Count(p[cut:], "\n")
	return p[:cut] + fmt.Sprintf("\n (… %d more lines not shown)", rest)
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	return clipText(s, n)
}

func short(s string) string { return oneLine(s, 140) }

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstParagraph(body string) string {
	body = strings.TrimSpace(body)
	if i := strings.Index(body, "\n\n"); i > 0 {
		body = body[:i]
	}
	return clipText(body, 800)
}

func clipList(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return append(in[:n:n], fmt.Sprintf("and %d more", len(in)-n))
}
