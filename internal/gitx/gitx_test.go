package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseRaw(t *testing.T) {
	out := ":100644 100644 aaa bbb M\x00src/a.ts\x00" +
		":000000 100755 000 ccc A\x00bin/run\x00" +
		":100644 100644 ddd eee R087\x00old name.md\x00new name.md\x00" +
		":100644 120000 fff 111 T\x00link\x00"
	got, err := ParseRaw([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{
		{SrcMode: "100644", DstMode: "100644", SrcOID: "aaa", DstOID: "bbb", Status: 'M', Path: "src/a.ts"},
		{SrcMode: "000000", DstMode: "100755", SrcOID: "000", DstOID: "ccc", Status: 'A', Path: "bin/run"},
		{SrcMode: "100644", DstMode: "100644", SrcOID: "ddd", DstOID: "eee", Status: 'R', Path: "new name.md", OldPath: "old name.md"},
		{SrcMode: "100644", DstMode: "120000", SrcOID: "fff", DstOID: "111", Status: 'T', Path: "link"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if got[3].Regular() || !got[1].Regular() {
		t.Fatal("Regular()")
	}
}

func TestParseUnified(t *testing.T) {
	out := strings.Join([]string{
		"diff --git a/a.txt b/a.txt",
		"index 111..222 100644",
		"--- a/a.txt",
		"+++ b/a.txt",
		"@@ -2 +2,2 @@ func heading",
		"-old",
		"+++ looks like a header but is content",
		"+new",
		"@@ -9,0 +11 @@",
		"+appended",
		"\\ No newline at end of file",
		"diff --git a/gone.txt b/gone.txt",
		"deleted file mode 100644",
		"--- a/gone.txt",
		"+++ /dev/null",
		"@@ -1 +0,0 @@",
		"-bye",
		`diff --git "a/tab\there.md" "b/tab\there.md"`,
		"new file mode 100644",
		"--- /dev/null",
		`+++ "b/tab\there.md"`,
		"@@ -0,0 +1,3 @@",
		"+x",
		"+y",
		"+z",
		"diff --git a/bin.png b/bin.png",
		"Binary files a/bin.png and b/bin.png differ",
		"",
	}, "\n")
	got, err := ParseUnified([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]Hunk{
		"a.txt":        {{2, 1, 2, 2}, {9, 0, 11, 1}},
		"tab\there.md": {{0, 0, 1, 3}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if lines := AddedLines(got["a.txt"]); !reflect.DeepEqual(lines, [][2]int{{2, 3}, {11, 11}}) {
		t.Fatalf("AddedLines = %v", lines)
	}
}

func TestMapLine(t *testing.T) {
	hunks := []Hunk{
		{OldStart: 0, OldCount: 0, NewStart: 1, NewCount: 1},  // insert at top
		{OldStart: 2, OldCount: 1, NewStart: 3, NewCount: 2},  // replace line 2 with two
		{OldStart: 5, OldCount: 0, NewStart: 7, NewCount: 2},  // insert two after 5
		{OldStart: 8, OldCount: 2, NewStart: 11, NewCount: 0}, // delete 8-9
	}
	for n, want := range map[int]int{1: 2, 2: 0, 3: 5, 5: 7, 6: 10, 7: 11, 8: 0, 9: 0, 10: 12} {
		if got := MapLine(hunks, n); got != want {
			t.Errorf("MapLine(%d) = %d, want %d", n, got, want)
		}
	}
	if MapLine(nil, 4) != 4 {
		t.Fatal("identity")
	}
}

func TestUnquoteC(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:           "plain",
		`"b/a\tb"`:        "b/a\tb",
		`"b/q\"uote\\"`:   `b/q"uote\`,
		`"b/caf\303\251"`: "b/caf\xc3\xa9",
	} {
		got, err := unquoteC(in)
		if err != nil || got != want {
			t.Errorf("unquoteC(%q) = %q, %v", in, got, err)
		}
	}
}

func TestParseVersion(t *testing.T) {
	for in, want := range map[string][2]int{
		"git version 2.53.0":                 {2, 53},
		"git version 2.39.3 (Apple Git-146)": {2, 39},
		"git version 2.45.1.windows.1":       {2, 45},
	} {
		ma, mi, err := ParseVersion(in)
		if err != nil || ma != want[0] || mi != want[1] {
			t.Errorf("ParseVersion(%q) = %d.%d, %v", in, ma, mi, err)
		}
	}
}

func TestChunks(t *testing.T) {
	var paths []string
	for i := 0; i < 5000; i++ {
		paths = append(paths, strings.Repeat("x", 20))
	}
	chunks := Chunks(paths)
	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	if len(chunks) < 2 || total != len(paths) {
		t.Fatalf("%d chunks, %d paths", len(chunks), total)
	}
}

// TestAgainstGit exercises the process-level helpers on a real repository.
func TestAgainstGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	g := Git{Env: []string{"GIT_CONFIG_GLOBAL=" + filepath.Join(dir, "gitconfig"), "GIT_CONFIG_NOSYSTEM=1"}}
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	mustRun := func(args ...string) {
		t.Helper()
		if _, err := g.Run(nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustRun("init", "-q")
	os.WriteFile(".gitattributes", []byte("*.gen -deslop\nlocked/** deslop=invisible\n"), 0o644)
	os.MkdirAll("locked", 0o755)
	os.WriteFile("a.gen", []byte("x\n"), 0o644)
	os.WriteFile("locked/b.txt", []byte("y\n"), 0o644)
	os.WriteFile("c.txt", []byte("z\n"), 0o644)
	mustRun("add", ".")

	attrs, err := g.CheckAttr([]string{"a.gen", "locked/b.txt", "c.txt"}, []string{"deslop", "filter"}, "--cached")
	if err != nil {
		t.Fatal(err)
	}
	if attrs["a.gen"]["deslop"] != "unset" || attrs["locked/b.txt"]["deslop"] != "invisible" || attrs["c.txt"]["deslop"] != "unspecified" {
		t.Fatalf("attrs = %v", attrs)
	}

	entries, err := g.LsFiles([]string{"c.txt", "locked/b.txt"})
	if err != nil || len(entries) != 2 || entries[0].Tag != 'H' || entries[0].Mode != "100644" {
		t.Fatalf("ls-files: %+v %v", entries, err)
	}

	oid, err := g.HashObject([]byte("hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	var got []Blob
	err = g.ReadBlobs([]string{oid, entries[0].OID, strings.Repeat("0", len(oid))}, 3, func(b Blob) error {
		got = append(got, b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !got[0].TooBig || string(got[1].Data) != "z\n" || !got[2].Missing {
		t.Fatalf("blobs: %+v", got)
	}

	if err := g.UpdateIndex([]IndexUpdate{{Mode: "100644", OID: oid, Path: "c.txt"}}); err != nil {
		t.Fatal(err)
	}
	staged, err := g.Run(nil, "show", ":c.txt")
	if err != nil || string(staged) != "hello\n" {
		t.Fatalf("index not updated: %q %v", staged, err)
	}
}
