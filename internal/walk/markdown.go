package walk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Event is one moment in how the pull request was built: a commit, or an
// edit a coding agent made (from Pocket AI).
type Event struct {
	Kind   string  `json:"kind"` // commit | edit
	At     string  `json:"at"`
	Title  string  `json:"title"`
	Body   string  `json:"body,omitempty"`
	SHA    string  `json:"sha,omitempty"`
	Author string  `json:"author,omitempty"`
	Files  []TFile `json:"files"`
}

// TFile is a file an event touched, with what that event changed in it.
type TFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch,omitempty"`
	Note      string `json:"note,omitempty"`
}

// FileAnchor is GitHub's anchor for a file in a pull request's Files tab.
func FileAnchor(prURL, path string) string {
	h := sha256.Sum256([]byte(path))
	return strings.TrimRight(prURL, "/") + "/files#diff-" + hex.EncodeToString(h[:])
}

// Markdown writes the walkthrough for a pull request comment or a document.
func Markdown(d *Doc, prURL, title string, link func(path string) string) string {
	if link == nil {
		link = func(p string) string { return FileAnchor(prURL, p) }
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Walkthrough: %s\n\n", title)
	if d.Overview != "" {
		fmt.Fprintf(&b, "**In one minute.** %s\n\n", d.Overview)
	}
	if d.Before != "" || d.After != "" {
		if d.Before != "" {
			fmt.Fprintf(&b, "- **Before:** %s\n", d.Before)
		}
		if d.After != "" {
			fmt.Fprintf(&b, "- **After:** %s\n", d.After)
		}
		b.WriteString("\n")
	}
	if len(d.Risks) > 0 {
		b.WriteString("**Worth a careful look**\n\n")
		for _, r := range d.Risks {
			fmt.Fprintf(&b, "- %s %s\n", riskMark(r.Level), r.Text)
		}
		b.WriteString("\n")
	}
	b.WriteString("### Read it in this order\n\n")
	for i, s := range d.Steps {
		fmt.Fprintf(&b, "**%d. %s**", i+1, s.Title)
		if s.Why != "" {
			fmt.Fprintf(&b, " — %s", s.Why)
		}
		b.WriteString("\n\n")
		for _, u := range s.Units {
			part := ""
			if len(u.Hunks) > 0 {
				part = " (part)"
			}
			fmt.Fprintf(&b, "- [`%s`](%s)%s — %s\n", u.Path, link(u.Path), part, u.Summary)
		}
		for _, r := range s.Risks {
			fmt.Fprintf(&b, "  - ⚠ %s\n", r)
		}
		b.WriteString("\n")
	}
	if len(d.Noise) > 0 {
		names := make([]string, 0, len(d.Noise))
		for _, u := range d.Noise {
			names = append(names, "`"+u.Path+"`")
		}
		fmt.Fprintf(&b, "<details><summary>Generated and lock files (%d)</summary>\n\n%s\n\n</details>\n\n", len(d.Noise), strings.Join(names, ", "))
	}
	if len(d.Questions) > 0 {
		b.WriteString("### Questions for the author\n\n")
		for _, q := range d.Questions {
			fmt.Fprintf(&b, "- %s\n", q)
		}
		b.WriteString("\n")
	}
	b.WriteString("<sub>Made with Pocket PR Review.</sub>\n")
	return b.String()
}

func riskMark(level string) string {
	switch level {
	case "high":
		return "🔴"
	case "low":
		return "⚪"
	}
	return "🟠"
}
