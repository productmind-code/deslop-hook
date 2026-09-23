// Package hook implements deslop-hook's commands on top of git plumbing:
// scrubbing the staged lines of a commit, checking a range or the whole
// tree, and fixing files in the working tree.
package hook

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/productmind-code/deslop-hook/internal/config"
	"github.com/productmind-code/deslop-hook/internal/gitx"
	"github.com/productmind-code/deslop-hook/internal/scrub"
)

// Mode selects what a run reads and what it may write.
type Mode int

const (
	// Hook scrubs the lines a commit adds, in the index and the working tree.
	Hook Mode = iota
	// WorktreeOnly scrubs the lines a commit adds in the working tree only.
	// Used under the pre-commit framework, which stashes unstaged changes
	// and would fight an index rewrite.
	WorktreeOnly
	// Report lists what Hook would change without changing it, and never
	// fails. Used by the global hook in repositories that have not opted in.
	Report
	// CheckStaged is the read-only form of Hook; it fails on findings.
	CheckStaged
	// CheckBase checks the lines added since a base revision (CI).
	CheckBase
	// CheckFiles checks whole files.
	CheckFiles
	// FixFiles rewrites whole files in the working tree.
	FixFiles
)

func (m Mode) writesIndex() bool    { return m == Hook }
func (m Mode) writesWorktree() bool { return m == Hook || m == WorktreeOnly || m == FixFiles }
func (m Mode) staged() bool         { return m == Hook || m == WorktreeOnly || m == Report || m == CheckStaged }

// Options configure a run.
type Options struct {
	Mode    Mode
	Base    string   // CheckBase
	All     bool     // CheckFiles/FixFiles: the whole tree
	Paths   []string // files to look at, relative to the top of the work tree
	DryRun  bool     // FixFiles: report only
	Format  string   // "text" or "github"
	Verbose bool
}

// Runner holds the state shared by every command.
type Runner struct {
	Git       gitx.Git
	Cfg       *config.Config
	Scrub     *scrub.Scrubber
	Top       string // absolute path of the work tree
	GitDir    string // absolute path of this work tree's git dir
	IndexFile string // absolute GIT_INDEX_FILE during a commit, else ""
	RealIndex string // the repository's own index file
	Stderr    io.Writer
	Stdout    io.Writer
}

// Setup finds the repository, moves to the top of its work tree and loads
// the configuration. Relative paths in the git environment are made absolute
// first, since they are relative to the directory git started the hook in.
func Setup(stdout, stderr io.Writer) (*Runner, error) {
	for _, k := range []string{"GIT_INDEX_FILE", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY"} {
		if v := os.Getenv(k); v != "" && !filepath.IsAbs(v) {
			abs, err := filepath.Abs(v)
			if err != nil {
				return nil, err
			}
			os.Setenv(k, abs)
		}
	}
	g := gitx.Git{}
	top, err := g.Line("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, notARepo(err)
	}
	if top == "" {
		return nil, errors.New("not inside a git work tree")
	}
	if err := os.Chdir(top); err != nil {
		return nil, err
	}
	gitDir, err := g.Line("rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	realIndex, err := g.Without("GIT_INDEX_FILE").Line("rev-parse", "--git-path", "index")
	if err != nil {
		return nil, err
	}
	if realIndex, err = filepath.Abs(realIndex); err != nil {
		return nil, err
	}
	cfg, err := config.Load(".")
	if err != nil {
		return nil, err
	}
	return &Runner{
		Git:       g,
		Cfg:       cfg,
		Scrub:     scrub.New(cfg.Chars),
		Top:       filepath.FromSlash(top),
		GitDir:    gitDir,
		IndexFile: os.Getenv("GIT_INDEX_FILE"),
		RealIndex: realIndex,
		Stdout:    stdout,
		Stderr:    stderr,
	}, nil
}

func notARepo(err error) error {
	var ge *gitx.Error
	if errors.As(err, &ge) && strings.Contains(ge.Stderr, "not a git repository") {
		return errors.New("not inside a git repository")
	}
	return err
}

// Run executes one command and returns the process exit code.
func (r *Runner) Run(opts Options) (int, error) {
	var (
		files []FileResult
		err   error
	)
	switch {
	case opts.Mode.staged():
		files, err = r.staged(opts)
	case opts.Mode == CheckBase:
		files, err = r.base(opts)
	default:
		files, err = r.files(opts)
	}
	if err != nil {
		return 2, err
	}
	return r.report(opts, files), nil
}

// FileResult is the outcome for one file.
type FileResult struct {
	Path     string
	Findings []scrub.Finding
	Written  bool   // the new content was written (index and/or worktree)
	Skip     string // why the file was not looked at
	Warning  string // something the user should know, e.g. worktree left alone
}

func (f FileResult) fixed() int {
	n := 0
	for _, x := range f.Findings {
		if !x.Manual {
			n++
		}
	}
	return n
}

func (f FileResult) manual() int { return len(f.Findings) - f.fixed() }

// samePath compares two paths, resolving symlinks in their directories (so
// /tmp and /private/tmp on macOS agree) and ignoring case on Windows.
func samePath(a, b string) bool {
	norm := func(p string) string {
		p = filepath.Clean(p)
		if dir, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
			p = filepath.Join(dir, filepath.Base(p))
		}
		return p
	}
	a, b = norm(a), norm(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// partialCommitIndex returns the repository index lock to update as well,
// when the commit is a partial one (`git commit <paths>`). Git then builds
// the commit from a temporary index (next-index-*.lock) and, after the
// commit, installs index.lock, which it wrote before the hook ran. Scrubbing
// only the temporary index would leave the real index with the old content.
func (r *Runner) partialCommitIndex() string {
	if r.IndexFile == "" || samePath(r.IndexFile, r.RealIndex) || samePath(r.IndexFile, r.RealIndex+".lock") {
		return ""
	}
	if !strings.HasPrefix(filepath.Base(r.IndexFile), "next-index") {
		return "" // a custom GIT_INDEX_FILE: leave the real index alone
	}
	lock := r.RealIndex + ".lock"
	if _, err := os.Stat(lock); err != nil {
		return ""
	}
	return lock
}

func (r *Runner) warnf(format string, args ...any) {
	fmt.Fprintf(r.Stderr, "deslop-hook: "+format+"\n", args...)
}
