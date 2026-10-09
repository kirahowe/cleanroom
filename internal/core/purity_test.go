package core

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// pureImports is everything the decision core may import. None of these
// packages can read a file, the clock, the environment or the network.
// (Package time is used only for the time.Time type and for parsing.)
var pureImports = map[string]bool{
	"bytes": true, "crypto/sha256": true, "encoding/hex": true, "encoding/json": true,
	"errors": true, "fmt": true, "maps": true, "regexp": true, "slices": true, "strconv": true,
	"strings": true, "time": true, "unicode/utf8": true,
	"cleanroom/internal/typed": true,
}

// TestCoreIsPure fails if the decision core or the type checker imports
// anything outside that list. "The decision is a pure function" is a claim
// the README makes, and this is what keeps it true.
func TestCoreIsPure(t *testing.T) {
	for _, dir := range []string{".", "../typed"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no Go files in %s (%v)", dir, err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				if !pureImports[path] {
					t.Errorf("%s imports %q, which is not on the list of pure packages", file, path)
				}
			}
		}
	}
}

// TestCoreDoesNotReadTheClock covers the one gap in the import list: the
// core may import package time for the Time type, but must not ask what
// time it is.
func TestCoreDoesNotReadTheClock(t *testing.T) {
	for _, dir := range []string{".", "../typed"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for _, banned := range []string{"time.Now", "time.Since", "time.Until", "time.Sleep", "time.After", "time.Tick"} {
				if strings.Contains(string(src), banned) {
					t.Errorf("%s uses %s", file, banned)
				}
			}
		}
	}
}
