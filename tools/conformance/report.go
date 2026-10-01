package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// report is the result of matching SPEC §17 bullets against TestSpec_ tests.
type report struct {
	specName string // file name shown with bullet line numbers
	bullets  []bullet
	covered  []bool // covered[i] is true when some valid test covers bullets[i]
	orphans  []orphan
	unparsed []parseFailure
}

// orphan is a TestSpec_ test that covers nothing as written.
type orphan struct {
	test    specTest
	problem string
}

// load reads the spec and the tests under root and matches them.
func load(root, specPath string) (report, error) {
	spec, err := os.ReadFile(specPath)
	if err != nil {
		return report{}, err
	}
	bullets, err := parseBullets(bytes.NewReader(spec))
	if err != nil {
		return report{}, fmt.Errorf("read %s: %w", specPath, err)
	}
	tests, unparsed, err := findSpecTests(root)
	if err != nil {
		return report{}, err
	}
	r := match(bullets, tests)
	r.unparsed = unparsed
	r.specName = filepath.Base(specPath)
	return r, nil
}

// match applies the ADR 0011 rule: a quote covers the one bullet in its section whose text starts
// with the quote. A test whose name section has no quote, or that has no quotes, covers nothing.
func match(bullets []bullet, tests []specTest) report {
	r := report{bullets: bullets, covered: make([]bool, len(bullets))}
	for _, t := range tests {
		if len(t.quotes) == 0 {
			r.orphans = append(r.orphans, orphan{t, `no SPEC §17.N: "…" quote in the doc comment`})
			continue
		}
		nameSection := "17." + specTestName.FindStringSubmatch(t.name)[1]
		if !hasSection(t.quotes, nameSection) {
			r.orphans = append(r.orphans, orphan{t, fmt.Sprintf("name says §%s but no quote is from §%s", nameSection, nameSection)})
			continue
		}
		for _, q := range t.quotes {
			hits := r.find(q)
			if len(hits) == 1 {
				r.covered[hits[0]] = true
				continue
			}
			what := "no bullet"
			if len(hits) > 1 {
				what = fmt.Sprintf("%d bullets", len(hits))
			}
			r.orphans = append(r.orphans, orphan{t, fmt.Sprintf("quote §%s %q matches %s", q.section, q.text, what)})
		}
	}
	return r
}

func hasSection(qs []quote, id string) bool {
	for _, q := range qs {
		if q.section == id {
			return true
		}
	}
	return false
}

// find returns the indexes of the bullets the quote is a prefix of.
func (r report) find(q quote) []int {
	var hits []int
	for i, b := range r.bullets {
		if b.section.id == q.section && strings.HasPrefix(b.text, q.text) {
			hits = append(hits, i)
		}
	}
	return hits
}

// gaps returns the bullets no valid test covers, in document order.
func (r report) gaps() []bullet {
	var gs []bullet
	for i, b := range r.bullets {
		if !r.covered[i] {
			gs = append(gs, b)
		}
	}
	return gs
}

// failsStrict reports whether -strict should fail: a core gap or any orphan. Integration gaps are
// listed but never fail, because those profiles are RECOMMENDED.
func (r report) failsStrict() bool {
	if len(r.orphans) > 0 {
		return true
	}
	for _, b := range r.gaps() {
		if b.section.profile == core {
			return true
		}
	}
	return false
}

const maxText = 100 // runes of bullet text shown per gap

// write prints the report and returns the first write error.
func (r report) write(w io.Writer) error {
	p := &printer{w: w}
	for _, prof := range []struct {
		name    string
		profile profile
	}{{"Core", core}, {"Integration", integration}} {
		total, done := 0, 0
		for i, b := range r.bullets {
			if b.section.profile == prof.profile {
				total++
				if r.covered[i] {
					done++
				}
			}
		}
		p.printf("%s: %d/%d bullets covered\n", prof.name, done, total)
	}

	if gs := r.gaps(); len(gs) > 0 {
		p.printf("\nBullets without a test (%d):\n", len(gs))
		last := ""
		for _, b := range gs {
			if b.section.id != last {
				p.printf("\n§%s %s\n", b.section.id, b.section.title)
				last = b.section.id
			}
			p.printf("  %s:%d  %s\n", r.specName, b.line, truncate(b.text, maxText))
		}
	}

	if len(r.orphans) > 0 {
		p.printf("\nTests that cover nothing (%d):\n", len(r.orphans))
		for _, o := range r.orphans {
			p.printf("  %s:%d  %s: %s\n", o.test.file, o.test.line, o.test.name, o.problem)
		}
	}

	if len(r.unparsed) > 0 {
		p.printf("\nTest files that don't parse, so their tests are not counted (%d):\n", len(r.unparsed))
		for _, u := range r.unparsed {
			p.printf("  %s\n", u.err)
		}
	}
	return p.err
}

// printer keeps the first write error so write can print without checking every line.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) printf(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, args...)
	}
}

func truncate(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n-1]) + "…"
}
