package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/productmind-code/deslop-hook/internal/gitx"
)

// Characters used by the tests, spelled as escapes so this file stays ASCII.
const (
	em    = "\u2014"
	lq    = "\u201c"
	rq    = "\u201d"
	nbsp  = "\u00a0"
	zwsp  = "\u200b"
	arrow = "\u2192"
)

var binDir, binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "deslop-hook-bin-")
	if err != nil {
		panic(err)
	}
	name := "deslop-hook"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", filepath.Join(dir, name), ".").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "building deslop-hook: %v\n%s", err, out)
		os.Exit(1)
	}
	binDir, binPath = dir, filepath.Join(dir, name)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type repo struct {
	t   *testing.T
	dir string
	env []string
}

// cleanEnv drops anything from the outer environment that would change
// git's or deslop-hook's behaviour: git variables (these tests may run inside
// a hook), CI (install stands down in CI), skips and pre-commit markers.
func cleanEnv(home string, extraPath string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(strings.ToUpper(k), "GIT_"), strings.EqualFold(k, "PATH"), strings.EqualFold(k, "HOME"),
			k == "CI", k == "DESLOP_SKIP", k == "PRE_COMMIT":
			continue // Windows spells PATH "Path"
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+home,
		"PATH="+extraPath+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, ".gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	os.MkdirAll(home, 0o755)
	r := &repo{t: t, dir: filepath.Join(root, "repo"), env: cleanEnv(home, binDir)}
	os.MkdirAll(r.dir, 0o755)
	r.git("init", "-q", "-b", "main")
	r.git("config", "core.autocrlf", "false")
	return r
}

func (r *repo) run(dir string, extraEnv []string, name string, args ...string) (string, string, error) {
	if name == "deslop-hook" {
		name = binPath // exec looks names up in our PATH, not cmd.Env's
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(append([]string{}, r.env...), extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	out, errOut, err := r.run(r.dir, nil, "git", args...)
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s%s", strings.Join(args, " "), err, out, errOut)
	}
	return out
}

func (r *repo) gitEnv(env []string, args ...string) (string, string) {
	r.t.Helper()
	out, errOut, err := r.run(r.dir, env, "git", args...)
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s%s", strings.Join(args, " "), err, out, errOut)
	}
	return out, errOut
}

func (r *repo) deslop(args ...string) (string, string, int) {
	r.t.Helper()
	return r.deslopIn(r.dir, args...)
}

func (r *repo) deslopIn(dir string, args ...string) (string, string, int) {
	r.t.Helper()
	out, errOut, err := r.run(dir, nil, "deslop-hook", args...)
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatal(err)
	}
	return out, errOut, code
}

func (r *repo) write(path, content string) {
	r.t.Helper()
	p := filepath.Join(r.dir, filepath.FromSlash(path))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) read(path string) string {
	r.t.Helper()
	data, err := os.ReadFile(filepath.Join(r.dir, filepath.FromSlash(path)))
	if err != nil {
		r.t.Fatal(err)
	}
	return string(data)
}

func (r *repo) show(spec string) string {
	r.t.Helper()
	return r.git("show", spec)
}

func (r *repo) status() string {
	r.t.Helper()
	return r.git("status", "--porcelain")
}

// commitSkip commits without the hook (to set up history with slop in it).
func (r *repo) commitSkip(msg string) {
	r.t.Helper()
	r.gitEnv([]string{"DESLOP_SKIP=1"}, "commit", "-q", "-m", msg)
}

func (r *repo) install() {
	r.t.Helper()
	if _, errOut, code := r.deslop("install"); code != 0 {
		r.t.Fatalf("install: %s", errOut)
	}
}

func eq(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s:\n got %q\nwant %q", what, got, want)
	}
}

func TestPlainCommitCleansOnlyAddedLines(t *testing.T) {
	r := newRepo(t)
	r.write("a.md", "old"+em+"line\nkeep\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()

	r.write("a.md", "old"+em+"line\nnew "+lq+"quoted"+rq+" line"+em+"here\nkeep\n")
	r.git("add", ".")
	_, errOut := r.gitEnv(nil, "commit", "-q", "-m", "second")
	if !strings.Contains(errOut, "cleaned 3 characters in 1 file") {
		t.Fatalf("summary missing: %q", errOut)
	}
	want := "old" + em + "line\nnew \"quoted\" line - here\nkeep\n"
	eq(t, "HEAD", r.show("HEAD:a.md"), want)
	eq(t, "worktree", r.read("a.md"), want)
	eq(t, "status", r.status(), "")
}

func TestPartiallyStagedFileKeepsUnstagedChanges(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "one\ntwo\nthree\nfour\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()

	r.write("a.txt", "one\ntwo"+em+"staged\nthree\nfour\n")
	r.git("add", ".")
	// Unstaged edits: a new line, and another edit to the staged line.
	r.write("a.txt", "zero"+em+"unstaged\none\ntwo"+em+"staged\nthree\nfour"+em+"unstaged\n")
	r.git("commit", "-q", "-m", "partial")

	eq(t, "HEAD", r.show("HEAD:a.txt"), "one\ntwo - staged\nthree\nfour\n")
	eq(t, "worktree", r.read("a.txt"), "zero"+em+"unstaged\none\ntwo - staged\nthree\nfour"+em+"unstaged\n")
	eq(t, "status", r.status(), " M a.txt\n")
}

func TestStagedLineEditedAgainInWorktree(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "one\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()

	r.write("a.txt", "one\nfirst"+em+"draft\n")
	r.git("add", ".")
	r.write("a.txt", "one\nsecond"+em+"draft\n")
	_, errOut := r.gitEnv(nil, "commit", "-q", "-m", "x")
	if !strings.Contains(errOut, "unstaged edits") {
		t.Fatalf("expected a warning, got %q", errOut)
	}
	eq(t, "HEAD", r.show("HEAD:a.txt"), "one\nfirst - draft\n")
	eq(t, "worktree untouched", r.read("a.txt"), "one\nsecond"+em+"draft\n")
}

func TestCommitAll(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "one\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()

	r.write("a.txt", "one\ntwo"+nbsp+"spaces"+zwsp+"\n")
	r.git("commit", "-q", "-a", "-m", "all")
	eq(t, "HEAD", r.show("HEAD:a.txt"), "one\ntwo spaces\n")
	eq(t, "worktree", r.read("a.txt"), "one\ntwo spaces\n")
	eq(t, "status", r.status(), "")
}

func TestPartialCommitWithPaths(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.write("b.txt", "b\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()

	r.write("b.txt", "b\nstaged b"+em+"x\n")
	r.git("add", "b.txt")
	r.write("a.txt", "a\nonly a"+em+"y\n")
	r.git("commit", "-q", "-m", "only a", "--", "a.txt")

	eq(t, "HEAD a", r.show("HEAD:a.txt"), "a\nonly a - y\n")
	eq(t, "worktree a", r.read("a.txt"), "a\nonly a - y\n")
	eq(t, "index a", r.show(":a.txt"), "a\nonly a - y\n")
	eq(t, "HEAD b untouched", r.show("HEAD:b.txt"), "b\n")
	eq(t, "index b still staged", r.show(":b.txt"), "b\nstaged b"+em+"x\n")
	eq(t, "status", r.status(), "M  b.txt\n")
}

func TestAmendOnlyCleansNewChanges(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "base\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.write("a.txt", "base\nfrom first"+em+"commit\n")
	r.git("add", ".")
	r.commitSkip("first")
	r.install()

	r.write("a.txt", "base\nfrom first"+em+"commit\nfrom amend"+em+"x\n")
	r.git("add", ".")
	r.git("commit", "-q", "--amend", "-m", "amended")
	eq(t, "HEAD", r.show("HEAD:a.txt"), "base\nfrom first"+em+"commit\nfrom amend - x\n")
}

func TestMergeCleansOnlyResolutions(t *testing.T) {
	r := newRepo(t)
	base := "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n"
	r.write("a.txt", base)
	r.git("add", ".")
	r.commitSkip("base")
	r.git("checkout", "-q", "-b", "feature")
	r.write("a.txt", "F"+em+"1\nl2\nl3\nl4\nl5\nl6\nl7\nF"+em+"8\n")
	r.git("commit", "-q", "-a", "-m", "feature")
	r.git("checkout", "-q", "main")
	r.write("a.txt", "M1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\n")
	r.git("commit", "-q", "-a", "-m", "main")
	r.install()

	_, _, err := r.run(r.dir, nil, "git", "merge", "-q", "feature")
	if err == nil {
		t.Fatal("expected a conflict")
	}
	r.write("a.txt", "R"+em+"1\nl2\nl3\nl4\nl5\nl6\nl7\nF"+em+"8\n")
	r.git("add", "a.txt")
	r.git("commit", "-q", "--no-edit")
	eq(t, "HEAD", r.show("HEAD:a.txt"), "R - 1\nl2\nl3\nl4\nl5\nl6\nl7\nF"+em+"8\n")
	eq(t, "status", r.status(), "")
}

func TestAutoCRLF(t *testing.T) {
	r := newRepo(t)
	r.git("config", "core.autocrlf", "true")
	r.write("a.txt", "one\r\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()

	r.write("a.txt", "one\r\ntwo"+em+"x\r\nend"+em+"\r\n")
	r.git("add", ".")
	r.git("commit", "-q", "-m", "crlf")
	eq(t, "HEAD", r.show("HEAD:a.txt"), "one\ntwo - x\nend-\n")
	eq(t, "worktree", r.read("a.txt"), "one\r\ntwo - x\r\nend-\r\n")
	eq(t, "status", r.status(), "")
}

func TestSparseEntryIsNotTouched(t *testing.T) {
	r := newRepo(t)
	r.write("in/a.txt", "a\n")
	r.write("out/b.txt", "b\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()

	r.write("out/b.txt", "b\nnew"+em+"x\n")
	r.git("add", ".")
	r.git("update-index", "--skip-worktree", "out/b.txt")
	os.Remove(filepath.Join(r.dir, "out", "b.txt"))
	r.git("commit", "-q", "-m", "sparse")
	eq(t, "HEAD keeps the staged content", r.show("HEAD:out/b.txt"), "b\nnew"+em+"x\n")
	if _, err := os.Stat(filepath.Join(r.dir, "out", "b.txt")); err == nil {
		t.Fatal("file outside the sparse checkout was recreated")
	}
	eq(t, "status", r.status(), "")
}

func TestLinkedWorktree(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()
	wt := filepath.Join(filepath.Dir(r.dir), "linked")
	r.git("worktree", "add", "-q", "-b", "side", wt)

	os.WriteFile(filepath.Join(wt, "a.txt"), []byte("a\nlinked"+em+"x\n"), 0o644)
	if out, errOut, err := r.run(wt, nil, "git", "commit", "-q", "-a", "-m", "linked"); err != nil {
		t.Fatalf("%v %s %s", err, out, errOut)
	}
	out, _, _ := r.run(wt, nil, "git", "show", "HEAD:a.txt")
	eq(t, "HEAD", out, "a\nlinked - x\n")
}

func TestRenameOnlyCleansChangedLines(t *testing.T) {
	r := newRepo(t)
	body := strings.Repeat("filler line to keep rename detection happy\n", 20)
	r.write("old.txt", "keep"+em+"this\n"+body)
	r.git("add", ".")
	r.commitSkip("init")
	r.install()

	r.git("mv", "old.txt", "new.txt")
	r.write("new.txt", "keep"+em+"this\n"+body+"added"+em+"line\n")
	r.git("add", ".")
	r.git("commit", "-q", "-m", "rename")
	eq(t, "HEAD", r.show("HEAD:new.txt"), "keep"+em+"this\n"+body+"added - line\n")
}

func TestFileDeletedAfterStagingIsNotRecreated(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()

	r.write("a.txt", "a\nb"+em+"c\n")
	r.git("add", ".")
	os.Remove(filepath.Join(r.dir, "a.txt"))
	r.git("commit", "-q", "-m", "x")
	eq(t, "HEAD", r.show("HEAD:a.txt"), "a\nb - c\n")
	if _, err := os.Stat(filepath.Join(r.dir, "a.txt")); err == nil {
		t.Fatal("deleted file was recreated")
	}
}

func TestAttributesAndSkips(t *testing.T) {
	r := newRepo(t)
	r.write(".gitattributes", "legal.txt -deslop\ntests/** deslop=invisible\n*.lfs filter=lfs\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()

	slop := "a" + em + "b" + zwsp + "c\n"
	r.write("legal.txt", slop)
	r.write("tests/t.txt", slop)
	r.write("big.lfs", slop)
	r.write("blob.bin", "x\x00y"+slop)
	r.write("ok.txt", slop)
	r.git("add", ".")
	r.git("commit", "-q", "-m", "x")
	eq(t, "excluded", r.show("HEAD:legal.txt"), slop)
	eq(t, "invisible only", r.show("HEAD:tests/t.txt"), "a"+em+"bc\n")
	eq(t, "filter", r.show("HEAD:big.lfs"), slop)
	eq(t, "binary", r.show("HEAD:blob.bin"), "x\x00y"+slop)
	eq(t, "default", r.show("HEAD:ok.txt"), "a - bc\n")
}

func TestSymlinkSkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()
	if err := os.Symlink("target"+em+"name", filepath.Join(r.dir, "link")); err != nil {
		t.Fatal(err)
	}
	r.git("add", ".")
	r.git("commit", "-q", "-m", "link")
	eq(t, "symlink target", r.show("HEAD:link"), "target"+em+"name")
}

func TestPragmaAndQuoteSafety(t *testing.T) {
	r := newRepo(t)
	r.write("a.ts", "x\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()

	r.write("a.ts", "x\nconst a = \"keep"+em+"me\"; // deslop:ignore\nconst b = \"shown as "+lq+"this"+rq+"\";\n// comment "+lq+"fine"+rq+"\n")
	r.git("add", ".")
	_, errOut := r.gitEnv(nil, "commit", "-q", "-m", "x")
	if !strings.Contains(errOut, "left for you to fix") || !strings.Contains(errOut, "a.ts:3:") {
		t.Fatalf("expected a manual-fix note for line 3: %q", errOut)
	}
	eq(t, "HEAD", r.show("HEAD:a.ts"), "x\nconst a = \"keep"+em+"me\"; // deslop:ignore\nconst b = \"shown as "+lq+"this"+rq+"\";\n// comment \"fine\"\n")
}

func TestDeslopSkip(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()
	r.write("a.txt", "a\nb"+em+"c\n")
	r.git("add", ".")
	r.commitSkip("skipped")
	eq(t, "HEAD", r.show("HEAD:a.txt"), "a\nb"+em+"c\n")
}

func TestPreCommitFrameworkModeLeavesIndexAlone(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.git("add", ".")
	r.commitSkip("init")

	r.write("a.txt", "a\nb"+em+"c\n")
	r.git("add", ".")
	out, errOut, err := r.run(r.dir, []string{"PRE_COMMIT=1"}, "deslop-hook", "fix", "--staged-lines", "a.txt")
	if err != nil {
		t.Fatalf("%v %s %s", err, out, errOut)
	}
	eq(t, "index untouched", r.show(":a.txt"), "a\nb"+em+"c\n")
	eq(t, "worktree cleaned", r.read("a.txt"), "a\nb - c\n")
}

func TestHooksPathAndChaining(t *testing.T) {
	r := newRepo(t)
	r.git("config", "core.hooksPath", ".githooks")
	r.write(".githooks/pre-commit", "#!/bin/sh\necho existing-hook-ran >&2\nexit 0\n")
	os.Chmod(filepath.Join(r.dir, ".githooks", "pre-commit"), 0o755)
	r.write("a.txt", "a\n")
	r.git("add", "a.txt")
	r.commitSkip("init")
	r.install()

	hook := r.read(".githooks/pre-commit")
	if !strings.HasPrefix(hook, "#!/bin/sh\n# >>> deslop-hook >>>") || !strings.Contains(hook, "existing-hook-ran") {
		t.Fatalf("block not inserted after the shebang:\n%s", hook)
	}
	r.write("a.txt", "a\nb"+em+"c\n")
	r.git("add", "a.txt")
	_, errOut := r.gitEnv(nil, "commit", "-q", "-m", "x")
	if !strings.Contains(errOut, "existing-hook-ran") {
		t.Fatalf("existing hook did not run: %q", errOut)
	}
	eq(t, "HEAD", r.show("HEAD:a.txt"), "a\nb - c\n")

	// Install twice: still one block. Uninstall: the original hook is back.
	r.install()
	if n := strings.Count(r.read(".githooks/pre-commit"), "# >>> deslop-hook >>>"); n != 1 {
		t.Fatalf("%d blocks after a second install", n)
	}
	r.deslop("uninstall")
	eq(t, "restored hook", r.read(".githooks/pre-commit"), "#!/bin/sh\necho existing-hook-ran >&2\nexit 0\n")
}

func TestChainsNonShellHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shebang dispatch differs on Windows")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	r := newRepo(t)
	hookPath := filepath.Join(r.dir, ".git", "hooks", "pre-commit")
	original := "#!" + python + "\nimport sys\nsys.stderr.write('python-hook-ran\\n')\n"
	os.WriteFile(hookPath, []byte(original), 0o755)
	r.write("a.txt", "a\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()

	r.write("a.txt", "a\nb"+em+"c\n")
	r.git("add", ".")
	_, errOut := r.gitEnv(nil, "commit", "-q", "-m", "x")
	if !strings.Contains(errOut, "python-hook-ran") {
		t.Fatalf("chained hook did not run: %q", errOut)
	}
	eq(t, "HEAD", r.show("HEAD:a.txt"), "a\nb - c\n")
	r.deslop("uninstall")
	data, _ := os.ReadFile(hookPath)
	eq(t, "restored", string(data), original)
}

func TestMissingBinaryFailsOpen(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "a\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()
	hookPath := filepath.Join(r.dir, ".git", "hooks", "pre-commit")
	data, _ := os.ReadFile(hookPath)
	// Point the recorded binary somewhere that does not exist. Replace the
	// quoted path itself: on Windows it is the long form of the temp dir,
	// while binDir may be the 8.3 short form (C:\Users\RUNNER~1\...).
	recorded := regexp.MustCompile(`for deslop_hook_try in '[^']*'`)
	if !recorded.Match(data) {
		t.Fatalf("no recorded binary in the hook:\n%s", data)
	}
	os.WriteFile(hookPath, recorded.ReplaceAll(data, []byte("for deslop_hook_try in '/nonexistent/deslop-hook'")), 0o755)

	r.write("a.txt", "a\nb"+em+"c\n")
	r.git("add", ".")
	env := []string{"PATH=" + os.Getenv("PATH")} // without binDir
	_, errOut := r.gitEnv(env, "commit", "-q", "-m", "x")
	if !strings.Contains(errOut, "deslop-hook: not found, skipping") {
		t.Fatalf("expected a warning: %q", errOut)
	}
	eq(t, "committed as is", r.show("HEAD:a.txt"), "a\nb"+em+"c\n")
}

func TestCheckBaseForCI(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", "old"+em+"slop\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.git("checkout", "-q", "-b", "pr")
	r.write("a.txt", "old"+em+"slop\nnew"+arrow+"slop\n")
	r.git("commit", "-q", "-a", "-m", "pr")

	out, _, code := r.deslop("check", "--base", "main")
	if code != 1 || !strings.Contains(out, "a.txt:2:4: U+2192 RIGHTWARDS ARROW") || strings.Contains(out, "a.txt:1:") {
		t.Fatalf("code %d, out %q", code, out)
	}
	summary := filepath.Join(r.dir, "..", "summary.md")
	o, _, err := r.run(r.dir, []string{"GITHUB_STEP_SUMMARY=" + summary}, "deslop-hook", "check", "--base", "main", "--format", "github")
	if err == nil || !strings.Contains(o, "::error file=a.txt,line=2,col=4,title=deslop-hook::") {
		t.Fatalf("github format: %v %q", err, o)
	}
	if data, _ := os.ReadFile(summary); !strings.Contains(string(data), "`a.txt:2:4`") {
		t.Fatalf("step summary: %q", data)
	}
}

func TestFixAllAndCheckAll(t *testing.T) {
	r := newRepo(t)
	r.write(".gitattributes", "keep.txt -deslop\n")
	r.write("a.md", "one"+em+"two "+lq+"q"+rq+"\n")
	r.write("sub/b.txt", "x"+nbsp+"y\n")
	r.write("keep.txt", "k"+em+"k\n")
	r.git("add", ".")
	r.commitSkip("init")

	if _, _, code := r.deslop("check", "--all"); code != 1 {
		t.Fatalf("check --all should fail, got %d", code)
	}
	out, _, _ := r.deslop("fix", "--all", "--dry-run")
	if !strings.Contains(out, "a.md:1:4") || r.read("a.md") != "one"+em+"two "+lq+"q"+rq+"\n" {
		t.Fatalf("dry run: %q", out)
	}
	if _, errOut, code := r.deslop("fix", "--all"); code != 0 || !strings.Contains(errOut, "fixed 4 characters in 2 files") {
		t.Fatalf("fix --all: %d %q", code, errOut)
	}
	eq(t, "a.md", r.read("a.md"), "one - two \"q\"\n")
	eq(t, "sub/b.txt", r.read("sub/b.txt"), "x y\n")
	eq(t, "keep.txt", r.read("keep.txt"), "k"+em+"k\n")
	if _, _, code := r.deslop("check", "--all"); code != 0 {
		t.Fatalf("check --all after fix: %d", code)
	}
	eq(t, "nothing staged", r.git("diff", "--cached", "--name-only"), "")
}

func TestPathsRelativeToSubdirectory(t *testing.T) {
	r := newRepo(t)
	r.write("sub/a.txt", "a"+em+"b\n")
	r.write("top.txt", "c"+em+"d\n")
	r.git("add", ".")
	r.commitSkip("init")
	out, _, code := r.deslopIn(filepath.Join(r.dir, "sub"), "check", "a.txt", "../top.txt")
	if code != 1 || !strings.Contains(out, "sub/a.txt:1:2") || !strings.Contains(out, "top.txt:1:2") {
		t.Fatalf("%d %q", code, out)
	}
	if _, errOut, code := r.deslopIn(filepath.Join(r.dir, "sub"), "fix", "."); code != 0 {
		t.Fatalf("fix .: %q", errOut)
	}
	eq(t, "fixed in sub", r.read("sub/a.txt"), "a - b\n")
	eq(t, "untouched outside", r.read("top.txt"), "c"+em+"d\n")
}

func TestGlobalInstall(t *testing.T) {
	r := newRepo(t)
	if !(gitx.Git{}).AtLeast(2, 54) {
		_, errOut, code := r.deslop("install", "--global")
		if code == 0 || !strings.Contains(errOut, "needs git 2.54") {
			t.Fatalf("expected a clear error on old git: %d %q", code, errOut)
		}
		return
	}
	if _, errOut, code := r.deslop("install", "--global"); code != 0 {
		t.Fatalf("install --global: %q", errOut)
	}
	r.write("a.txt", "a\n")
	r.git("add", ".")
	r.commitSkip("init")

	// Not opted in: report only.
	r.write("a.txt", "a\nb"+em+"c\n")
	r.git("add", ".")
	_, errOut := r.gitEnv(nil, "commit", "-q", "-m", "x")
	if !strings.Contains(errOut, "has not opted in") {
		t.Fatalf("expected a report: %q", errOut)
	}
	eq(t, "untouched", r.show("HEAD:a.txt"), "a\nb"+em+"c\n")

	// Opted in with a config file: fixed, and only once even with the
	// repository hook installed too.
	r.write(".deslop-hook.toml", "")
	r.install()
	r.write("a.txt", "a\nb"+em+"c\nd"+em+"e\n")
	r.git("add", ".")
	_, errOut = r.gitEnv(nil, "commit", "-q", "-m", "y")
	if strings.Count(errOut, "cleaned") != 1 {
		t.Fatalf("expected exactly one run: %q", errOut)
	}
	eq(t, "fixed", r.show("HEAD:a.txt"), "a\nb"+em+"c\nd - e\n")
}

func TestSHA256Repository(t *testing.T) {
	r := newRepo(t)
	os.RemoveAll(filepath.Join(r.dir, ".git"))
	if _, errOut, err := r.run(r.dir, nil, "git", "init", "-q", "--object-format=sha256"); err != nil {
		t.Skipf("git without SHA-256 support: %s", errOut)
	}
	r.write("a.txt", "a\n")
	r.git("add", ".")
	r.commitSkip("init")
	r.install()
	r.write("a.txt", "a\nb"+em+"c\n")
	r.git("add", ".")
	r.git("commit", "-q", "-m", "x")
	eq(t, "HEAD", r.show("HEAD:a.txt"), "a\nb - c\n")
	eq(t, "status", r.status(), "")
}
