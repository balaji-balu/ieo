package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
)

// quote is one `SPEC §17.N: "…"` line in a test's doc comment: the opening words of the bullet
// the test covers (G-F2, ADR 0011).
type quote struct {
	section string // "17.4"
	text    string // whitespace collapsed
}

// specTest is a TestSpec_17_N_… function.
type specTest struct {
	name   string
	file   string // slash-separated, relative to the repository root
	line   int
	quotes []quote
}

// parseFailure is a _test.go file that could not be parsed, so its tests are invisible.
type parseFailure struct {
	file string
	err  string
}

var (
	specTestName = regexp.MustCompile(`^TestSpec_17_(\d+)_`)
	specQuote    = regexp.MustCompile(`SPEC §(17\.\d+): "([^"]+)"`)
)

// findSpecTests parses every _test.go file under root and returns its TestSpec_17_ functions.
// Files that don't parse are returned as failures, not errors, so one broken package does not hide
// the rest of the report.
func findSpecTests(root string) ([]specTest, []parseFailure, error) {
	var (
		tests    []specTest
		unparsed []parseFailure
		fset     = token.NewFileSet()
	)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		found, failure := scanFile(fset, path, rel)
		if failure != nil {
			unparsed = append(unparsed, *failure)
		}
		tests = append(tests, found...)
		return nil
	})
	return tests, unparsed, err
}

// scanFile returns the TestSpec_17_ functions of one file, or a failure if it doesn't parse.
func scanFile(fset *token.FileSet, path, rel string) ([]specTest, *parseFailure) {
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, &parseFailure{file: rel, err: err.Error()}
	}
	var tests []specTest
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !specTestName.MatchString(fn.Name.Name) {
			continue
		}
		tests = append(tests, specTest{
			name:   fn.Name.Name,
			file:   rel,
			line:   fset.Position(fn.Pos()).Line,
			quotes: quotes(fn.Doc),
		})
	}
	return tests, nil
}

// skipDir reports directories the go tool ignores too: testdata, vendor, and names starting with
// "." or "_".
func skipDir(name string) bool {
	return name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// quotes returns the SPEC quotes in a doc comment. A quote may wrap across comment lines.
func quotes(doc *ast.CommentGroup) []quote {
	if doc == nil {
		return nil
	}
	var qs []quote
	for _, m := range specQuote.FindAllStringSubmatch(normalize(doc.Text()), -1) {
		qs = append(qs, quote{section: m[1], text: m[2]})
	}
	return qs
}
