package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fixtureRoot = "testdata/repo"

func TestParseBullets(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join(fixtureRoot, "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := parseBullets(bytes.NewReader(spec))
	if err != nil {
		t.Fatalf("parseBullets: %v", err)
	}

	type row struct {
		section string
		profile profile
		line    int
		text    string
	}
	want := []row{
		{"17.1", core, 11, "Device ID parsing splits on the first `/` only."},
		{"17.1", core, 12, "Site IDs with characters outside RFC 3986 unreserved are rejected; `any` is rejected as a site ID."},
		{"17.2", core, 17, "First sync stores the manifest."},
		{"17.2", core, 18, "First sync with an empty bundle stores nothing."},
		{"17.2", core, 19, "Unmatched bullet stays a gap."},
		{"17.8", integration, 23, "Golden path: import app → deploy directed → delete."},
	}
	var rows []row
	for _, b := range got {
		rows = append(rows, row{b.section.id, b.section.profile, b.line, b.text})
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("bullets:\n got %+v\nwant %+v", rows, want)
	}
}

func TestFindSpecTests(t *testing.T) {
	tests, unparsed, err := findSpecTests(fixtureRoot)
	if err != nil {
		t.Fatalf("findSpecTests: %v", err)
	}

	gotQuotes := map[string][]quote{}
	for _, st := range tests {
		gotQuotes[st.name] = st.quotes
	}
	wantQuotes := map[string][]quote{
		"TestSpec_17_1_DeviceIDSplitsOnFirstSlash": {{"17.1", "Device ID parsing splits on the first"}},
		"TestSpec_17_1_SiteIDs": {
			{"17.1", "Site IDs with characters outside RFC 3986 unreserved are rejected"},
			{"17.2", "First sync stores"},
		},
		"TestSpec_17_2_Ambiguous":    {{"17.2", "First sync"}},
		"TestSpec_17_2_Orphan":       {{"17.2", "No such bullet"}},
		"TestSpec_17_2_NoQuote":      nil,
		"TestSpec_17_2_WrongSection": {{"17.1", "Device ID parsing"}},
		"TestSpec_17_8_GoldenPath":   {{"17.8", "Golden path"}},
	}
	if !reflect.DeepEqual(gotQuotes, wantQuotes) {
		t.Errorf("quotes:\n got %+v\nwant %+v", gotQuotes, wantQuotes)
	}

	if len(unparsed) != 1 || unparsed[0].file != "pkg/broken/broken_test.go" {
		t.Errorf("unparsed = %+v, want one entry for pkg/broken/broken_test.go", unparsed)
	}
}

func TestMatch(t *testing.T) {
	r := loadFixtureReport(t)

	var gaps []string
	for _, b := range r.gaps() {
		gaps = append(gaps, b.text)
	}
	wantGaps := []string{"First sync with an empty bundle stores nothing.", "Unmatched bullet stays a gap."}
	if !reflect.DeepEqual(gaps, wantGaps) {
		t.Errorf("gaps:\n got %q\nwant %q", gaps, wantGaps)
	}

	problems := map[string]string{}
	for _, o := range r.orphans {
		problems[o.test.name] = o.problem
	}
	wantProblems := map[string]string{
		"TestSpec_17_2_Ambiguous":    `quote §17.2 "First sync" matches 2 bullets`,
		"TestSpec_17_2_Orphan":       `quote §17.2 "No such bullet" matches no bullet`,
		"TestSpec_17_2_NoQuote":      `no SPEC §17.N: "…" quote in the doc comment`,
		"TestSpec_17_2_WrongSection": "name says §17.2 but no quote is from §17.2",
	}
	if !reflect.DeepEqual(problems, wantProblems) {
		t.Errorf("orphans:\n got %q\nwant %q", problems, wantProblems)
	}
}

func TestReportOutput(t *testing.T) {
	var out bytes.Buffer
	if err := loadFixtureReport(t).write(&out); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := out.String()

	for _, want := range []string{
		"Core: 3/5 bullets covered",
		"Integration: 1/1 bullets covered",
		"§17.2 Sync",
		"SPEC.md:18  First sync with an empty bundle stores nothing.",
		"pkg/a/a_test.go:19  TestSpec_17_2_Orphan: quote §17.2 \"No such bullet\" matches no bullet",
		// Every path is repo-relative and slash-separated, parse errors included (ADR 0009).
		"\n  pkg/broken/broken_test.go:3:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}

func TestRunExitCodes(t *testing.T) {
	covered := t.TempDir()
	writeFile(t, filepath.Join(covered, "SPEC.md"), "### 17.1 Contracts\n\n- Only bullet.\n")
	writeFile(t, filepath.Join(covered, "x", "x_test.go"),
		"package x\n\nimport \"testing\"\n\n// SPEC §17.1: \"Only bullet.\"\nfunc TestSpec_17_1_Only(t *testing.T) {}\n")

	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"gaps, report only", []string{"-root", fixtureRoot}, 0},
		{"gaps, strict", []string{"-root", fixtureRoot, "-strict"}, 1},
		{"all covered, strict", []string{"-root", covered, "-strict"}, 0},
		{"missing spec", []string{"-root", t.TempDir()}, 2},
		{"bad flag", []string{"-nope"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(tc.args, &stdout, &stderr); got != tc.want {
				t.Errorf("run(%q) = %d, want %d\nstdout:\n%s\nstderr:\n%s", tc.args, got, tc.want, &stdout, &stderr)
			}
		})
	}
}

func loadFixtureReport(t *testing.T) report {
	t.Helper()
	r, err := load(fixtureRoot, filepath.Join(fixtureRoot, "SPEC.md"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return r
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
