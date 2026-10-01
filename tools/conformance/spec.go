package main

import (
	"bufio"
	"io"
	"regexp"
	"strings"
)

// profile is the §17 validation profile a bullet belongs to.
type profile int

const (
	// core bullets are REQUIRED for conformance (SPEC §17).
	core profile = iota
	// integration bullets belong to a RECOMMENDED profile (§17.8, §17.9).
	integration
)

// section is one "### 17.N Title" subsection of SPEC.md.
type section struct {
	id      string // "17.4"
	title   string // "LO: Reconciliation and Placement"
	profile profile
}

// bullet is one top-level list item of a §17 subsection: one requirement that needs a TestSpec_
// test (G-F2).
type bullet struct {
	section section
	line    int    // 1-based line of the "- " in SPEC.md
	text    string // the item's text, continuation lines joined, whitespace collapsed
}

var sectionHeading = regexp.MustCompile(`^### (17\.\d+) (.+)$`)

// parseBullets returns the bullets of every §17 subsection, in document order. Only top-level
// "- " items count: prose paragraphs and tables are not requirements (ADR 0010, §17.10).
// Subsections whose heading says "(RECOMMENDED)" are integration profiles; the rest are core.
func parseBullets(r io.Reader) ([]bullet, error) {
	var (
		bullets []bullet
		cur     *section // nil outside §17 subsections
		open    *bullet  // the item being read, until a blank line or the next item
	)
	flush := func() {
		if open != nil {
			open.text = normalize(open.text)
			bullets = append(bullets, *open)
			open = nil
		}
	}

	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "#"):
			flush()
			cur = nil
			if m := sectionHeading.FindStringSubmatch(line); m != nil {
				s := section{id: m[1], title: m[2], profile: core}
				if strings.Contains(m[2], "(RECOMMENDED)") {
					s.profile = integration
				}
				cur = &s
			}
		case cur == nil:
		case strings.HasPrefix(line, "- "):
			flush()
			open = &bullet{section: *cur, line: n, text: line[2:]}
		case open != nil && strings.HasPrefix(line, "  ") && strings.TrimSpace(line) != "":
			open.text += " " + line
		default:
			flush()
		}
	}
	flush()
	return bullets, sc.Err()
}

// normalize collapses runs of whitespace so wrapped text compares equal to unwrapped text.
func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
