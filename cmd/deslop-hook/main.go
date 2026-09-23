// Command deslop-hook removes the Unicode characters language models leave
// in code and docs (em dashes, curly quotes, zero-width and bidi controls,
// arrows...) from what you commit.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/productmind-code/deslop-hook/internal/gitx"
	"github.com/productmind-code/deslop-hook/internal/hook"
	"github.com/productmind-code/deslop-hook/internal/install"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `deslop-hook removes AI-slop characters (em dashes, curly quotes, zero-width
and bidi controls, arrows, ...) from what you commit.

Usage:
  deslop-hook install [--global]      install the pre-commit hook in this repository
                                      (--global: every repository, git 2.54+)
  deslop-hook uninstall [--global]    remove it
  deslop-hook check [flags] [paths]   report without changing anything; exits 1 on findings
      --staged        the lines added by the next commit (default)
      --base REV      the lines added since REV (use in CI: --base origin/main)
      --all           every tracked file, whole
      paths...        these files, whole
      --format F      text (default) or github (annotations + step summary)
  deslop-hook fix [flags] [paths]     rewrite working-tree files
      --all           every tracked file, whole: the one-off repository sweep
      paths...        these files, whole
      --dry-run       list what would change
  deslop-hook hook                    what the git hook runs: cleans the staged lines
  deslop-hook version

Common flags: -v (also list skipped files).
Skip one commit with DESLOP_SKIP=1 git commit ... or git commit --no-verify.
Exclude paths in .gitattributes: "vendor/** -deslop", or narrow them: "tests/** deslop=invisible".
Keep a line as it is with a deslop:ignore comment; see ` + install.ProjectURL + `
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, args := args[0], args[1:]
	var code int
	var err error
	switch cmd {
	case "hook":
		code, err = cmdHook(args, stdout, stderr)
	case "check":
		code, err = cmdCheck(args, stdout, stderr)
	case "fix":
		code, err = cmdFix(args, stdout, stderr)
	case "install", "uninstall":
		code, err = cmdInstall(cmd, args, stdout)
	case "version", "--version", "-V":
		fmt.Fprintln(stdout, "deslop-hook", buildVersion())
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "deslop-hook: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "deslop-hook: %v\n", err)
		if code == 0 {
			code = 2
		}
	}
	return code
}

func buildVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	return fs
}

// parse lets flags and paths be mixed ("fix a.md --dry-run").
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return rest, nil
		}
		if args[0] == "--" {
			return append(rest, args[1:]...), nil
		}
		rest = append(rest, args[0])
		args = args[1:]
	}
}

func truthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v != "" && v != "0" && v != "false" && v != "no"
}

func cmdHook(args []string, stdout, stderr io.Writer) (int, error) {
	fs := newFlags("hook", stderr)
	fromRepo := fs.Bool("from-repo-hook", false, "invoked by the repository hook")
	fromGlobal := fs.Bool("from-global", false, "invoked by the global config hook")
	verbose := fs.Bool("v", false, "list skipped files")
	if _, err := parse(fs, args); err != nil {
		return 2, err
	}
	if truthy(os.Getenv("DESLOP_SKIP")) {
		return 0, nil
	}
	g := gitx.Git{}
	if *fromRepo && install.GlobalActive(g) {
		return 0, nil // the global config hook already ran for this commit
	}
	if *fromGlobal && !install.Enabled(g) {
		return 0, nil
	}
	r, err := hook.Setup(stdout, stderr)
	if err != nil {
		return 1, err
	}
	mode := hook.Hook
	if os.Getenv("PRE_COMMIT") == "1" {
		mode = hook.WorktreeOnly
	}
	if *fromGlobal && !optedIn(r) {
		mode = hook.Report
	}
	code, err := r.Run(hook.Options{Mode: mode, Verbose: *verbose})
	if err != nil {
		return 1, fmt.Errorf("%w\n(to commit without deslop-hook: DESLOP_SKIP=1 git commit ...)", err)
	}
	return code, nil
}

// optedIn reports whether a repository asked for deslop-hook: a config
// file, a deslop attribute, or the repository hook.
func optedIn(r *hook.Runner) bool {
	if r.Cfg.Present {
		return true
	}
	if data, err := os.ReadFile(".gitattributes"); err == nil && bytes.Contains(data, []byte("deslop")) {
		return true
	}
	return install.Installed(r.Git)
}

func cmdCheck(args []string, stdout, stderr io.Writer) (int, error) {
	fs := newFlags("check", stderr)
	staged := fs.Bool("staged", false, "check the lines added by the next commit")
	base := fs.String("base", "", "check the lines added since this revision")
	all := fs.Bool("all", false, "check every tracked file")
	format := fs.String("format", "text", "text or github")
	verbose := fs.Bool("v", false, "list skipped files")
	cwd, _ := os.Getwd()
	paths, err := parse(fs, args)
	if err != nil {
		return 2, err
	}
	if *format != "text" && *format != "github" {
		return 2, fmt.Errorf("--format must be text or github")
	}
	n := 0
	for _, set := range []bool{*staged, *base != "", *all, len(paths) > 0} {
		if set {
			n++
		}
	}
	if n > 1 {
		return 2, errors.New("choose one of --staged, --base, --all or paths")
	}
	r, err := hook.Setup(stdout, stderr)
	if err != nil {
		return 2, err
	}
	opts := hook.Options{Mode: hook.CheckStaged, Format: *format, Verbose: *verbose}
	switch {
	case *base != "":
		opts.Mode, opts.Base = hook.CheckBase, *base
	case *all:
		opts.Mode, opts.All = hook.CheckFiles, true
	case len(paths) > 0:
		opts.Mode = hook.CheckFiles
		if opts.Paths, err = repoPaths(r.Top, cwd, paths); err != nil {
			return 2, err
		}
	}
	return r.Run(opts)
}

func cmdFix(args []string, stdout, stderr io.Writer) (int, error) {
	fs := newFlags("fix", stderr)
	all := fs.Bool("all", false, "fix every tracked file")
	stagedLines := fs.Bool("staged-lines", false, "fix only the staged lines, in the working tree (pre-commit framework)")
	dryRun := fs.Bool("dry-run", false, "list what would change")
	verbose := fs.Bool("v", false, "list skipped files")
	cwd, _ := os.Getwd()
	paths, err := parse(fs, args)
	if err != nil {
		return 2, err
	}
	r, err := hook.Setup(stdout, stderr)
	if err != nil {
		return 2, err
	}
	opts := hook.Options{Mode: hook.FixFiles, DryRun: *dryRun, Verbose: *verbose}
	switch {
	case *stagedLines:
		// The pre-commit framework passes the files it wants checked;
		// deslop-hook still limits itself to their staged lines.
		opts.Mode = hook.WorktreeOnly
		if *dryRun {
			opts.Mode = hook.CheckStaged
		}
	case *all && len(paths) > 0:
		return 2, errors.New("choose --all or paths, not both")
	case *all:
		opts.All = true
	case len(paths) == 0:
		return 2, errors.New("say what to fix: --all, or one or more paths")
	}
	if len(paths) > 0 {
		if opts.Paths, err = repoPaths(r.Top, cwd, paths); err != nil {
			return 2, err
		}
	}
	return r.Run(opts)
}

func cmdInstall(cmd string, args []string, stdout io.Writer) (int, error) {
	fs := newFlags(cmd, stdout)
	global := fs.Bool("global", false, "every repository, through ~/.gitconfig (git 2.54+)")
	quiet := fs.Bool("quiet", false, "no output; succeed silently outside a git work tree")
	if _, err := parse(fs, args); err != nil {
		return 2, err
	}
	opts := install.Options{Global: *global, Quiet: *quiet, Out: stdout}
	var err error
	if cmd == "install" {
		err = install.Install(gitx.Git{}, opts)
	} else {
		err = install.Uninstall(gitx.Git{}, opts)
	}
	if err != nil {
		return 2, err
	}
	return 0, nil
}

// repoPaths turns command-line paths (relative to where the command was
// started) into slash-separated paths relative to the top of the work tree.
func repoPaths(top, cwd string, paths []string) ([]string, error) {
	realTop := resolve(top)
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		abs := p
		if !filepath.IsAbs(p) {
			abs = filepath.Join(cwd, p)
		}
		rel, err := filepath.Rel(realTop, resolve(abs))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s is outside the repository", p)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out, nil
}

// resolve evaluates symlinks in p, or in its directory when p does not
// exist.
func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if d, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(d, filepath.Base(p))
	}
	return filepath.Clean(p)
}
