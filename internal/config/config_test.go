package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/productmind-code/deslop-hook/internal/scrub"
)

func TestLoadMissing(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil || c.Present || c.Groups != scrub.AllGroups || c.MaxFileSize != DefaultMaxFileSize || !c.Prose[".md"] {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(`
groups = ["invisible", "typography"]
prose_extensions = ["md", ".TXT"]
max_file_size = 1024

[chars]
"\u2192" = "keep"
"U+2014" = "--"
`), 0o644)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Present || c.Groups != scrub.Invisible|scrub.Typography || c.MaxFileSize != 1024 {
		t.Fatalf("%+v", c)
	}
	if !c.Prose[".md"] || !c.Prose[".txt"] || c.Prose[".mdx"] {
		t.Fatalf("prose %v", c.Prose)
	}
	if c.Chars[0x2192] != scrub.Keep || c.Chars[0x2014] != "--" {
		t.Fatalf("chars %v", c.Chars)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte("grups = [\"all\"]\n"), 0o644)
	if _, err := Load(dir); err == nil {
		t.Fatal("want error for a typo")
	}
}

func TestForPath(t *testing.T) {
	c := Default()
	cases := []struct {
		path   string
		attrs  map[string]string
		groups scrub.Group
		prose  bool
		skip   bool
	}{
		{"a.ts", map[string]string{"deslop": "unspecified"}, scrub.AllGroups, false, false},
		{"README.md", nil, scrub.AllGroups, true, false},
		{"a.ts", map[string]string{"deslop": "unset"}, 0, false, true},
		{"a.ts", map[string]string{"deslop": "invisible"}, scrub.Invisible, false, false},
		{"a.ts", map[string]string{"deslop": "set"}, scrub.AllGroups, false, false},
		{"a.bin", map[string]string{"filter": "lfs"}, 0, false, true},
		{"a.bin", map[string]string{"binary": "set"}, 0, false, true},
		{"a.bin", map[string]string{"text": "unset"}, 0, false, true},
		{"a.txt", map[string]string{"working-tree-encoding": "UTF-16LE"}, 0, false, true},
		{"gen.ts", map[string]string{"linguist-generated": "true"}, 0, false, true},
		{"a.ts", map[string]string{"deslop": "bogus"}, 0, false, true},
	}
	for _, tc := range cases {
		opt, skip := c.ForPath(tc.path, tc.attrs)
		if (skip != "") != tc.skip {
			t.Errorf("%s %v: skip=%q", tc.path, tc.attrs, skip)
			continue
		}
		if !tc.skip && (opt.Groups != tc.groups || opt.Prose != tc.prose) {
			t.Errorf("%s %v: got %+v", tc.path, tc.attrs, opt)
		}
	}
}

func TestBinary(t *testing.T) {
	if Binary([]byte("text\n")) || !Binary([]byte("a\x00b")) {
		t.Fatal("Binary")
	}
}
