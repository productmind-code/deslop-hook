// Package install puts deslop-hook into a repository's pre-commit hook, or
// into the global git config as a config-based hook (git 2.54+).
package install

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/productmind-code/deslop-hook/internal/gitx"
)

const (
	beginMarker = "# >>> deslop-hook >>>"
	endMarker   = "# <<< deslop-hook <<<"
	chainSuffix = ".deslop-chain"

	// HookName is the name of the config-based hook (hook.<name>.*).
	HookName = "deslop"
	// GlobalCommand is what the global config hook runs.
	GlobalCommand = "deslop-hook hook --from-global"
	// ProjectURL is shown when the binary cannot be found.
	ProjectURL = "https://github.com/productmind-code/deslop-hook"
)

// Options configure install and uninstall.
type Options struct {
	Global bool
	Quiet  bool
	Out    io.Writer
}

func (o Options) say(format string, args ...any) {
	if !o.Quiet {
		fmt.Fprintf(o.Out, "deslop-hook: "+format+"\n", args...)
	}
}

// inCI reports whether we are running in CI, where installing a hook is
// pointless (and, on self-hosted runners that keep their checkouts, would
// leave one behind).
func inCI() bool {
	v := strings.ToLower(os.Getenv("CI"))
	return v != "" && v != "false" && v != "0"
}

// Install installs the hook.
func Install(g gitx.Git, opts Options) error {
	if opts.Global {
		return installGlobal(g, opts)
	}
	if inCI() {
		opts.say("CI detected, not installing the hook")
		return nil
	}
	hookPath, err := hookFile(g)
	if err != nil {
		if opts.Quiet {
			return nil // e.g. `prepare` running in a tarball or a Docker build without .git
		}
		return err
	}
	if msg := manager(g, hookPath); msg != "" {
		opts.say("%s", msg)
		return nil
	}

	block := Block(recordedBinary())
	existing, err := os.ReadFile(hookPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
			return err
		}
		return writeHook(hookPath, "#!/bin/sh\n"+block, opts, "installed")
	case err != nil:
		return err
	}

	content := string(existing)
	if strings.Contains(content, beginMarker) {
		updated := replaceBlock(content, block)
		if updated == content {
			opts.say("already installed in %s", hookPath)
			return nil
		}
		return writeHook(hookPath, updated, opts, "updated")
	}
	if shellHook(content) {
		first, rest, _ := strings.Cut(content, "\n")
		return writeHook(hookPath, first+"\n"+block+rest, opts, "added to the existing hook")
	}

	// A hook in another language: keep it, run it after us.
	chain := hookPath + chainSuffix
	if _, err := os.Stat(chain); err == nil {
		return fmt.Errorf("%s already exists; move it away and try again", chain)
	}
	if err := os.Rename(hookPath, chain); err != nil {
		return err
	}
	wrapper := "#!/bin/sh\n" + block + "exec \"$(dirname \"$0\")/" + filepath.Base(chain) + "\" \"$@\"\n"
	return writeHook(hookPath, wrapper, opts, "installed (your existing hook now runs after it)")
}

// Block returns the shell snippet that runs deslop-hook. It never uses
// exec, so whatever follows it in the hook still runs, and it lets the
// commit through with a warning if the binary is missing.
func Block(recorded string) string {
	var b strings.Builder
	b.WriteString(beginMarker + "\n")
	b.WriteString("# Managed by `deslop-hook install`; remove with `deslop-hook uninstall`.\n")
	b.WriteString("deslop_hook_bin=\n")
	b.WriteString("for deslop_hook_try in " + shellQuote(recorded) + " \"$(command -v deslop-hook 2>/dev/null)\" ./node_modules/.bin/deslop-hook; do\n")
	b.WriteString("\tif [ -n \"$deslop_hook_try\" ] && [ -x \"$deslop_hook_try\" ]; then deslop_hook_bin=$deslop_hook_try; break; fi\n")
	b.WriteString("done\n")
	b.WriteString("if [ -n \"$deslop_hook_bin\" ]; then\n")
	b.WriteString("\t\"$deslop_hook_bin\" hook --from-repo-hook || exit $?\n")
	b.WriteString("else\n")
	b.WriteString("\techo \"deslop-hook: not found, skipping (install: " + ProjectURL + ")\" >&2\n")
	b.WriteString("fi\n")
	b.WriteString("unset deslop_hook_bin deslop_hook_try\n")
	b.WriteString(endMarker + "\n")
	return b.String()
}

// recordedBinary is the absolute path of the running binary. When installed
// through npm this is the native binary inside node_modules, which lets the
// hook run without Node on PATH (GUI git clients often lack it).
func recordedBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if strings.Contains(exe, "go-build") {
		return "" // `go run`: a temporary build
	}
	if runtime.GOOS == "windows" {
		exe = filepath.ToSlash(exe) // Git for Windows' sh understands C:/...
	}
	return exe
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellHook(content string) bool {
	first, _, _ := strings.Cut(content, "\n")
	first = strings.TrimSpace(first)
	if !strings.HasPrefix(first, "#!") {
		return false
	}
	f := strings.Fields(strings.TrimPrefix(first, "#!"))
	if len(f) == 0 {
		return false
	}
	interp := filepath.Base(f[0])
	if interp == "env" && len(f) > 1 {
		interp = f[1]
	}
	switch interp {
	case "sh", "bash", "dash", "zsh", "ksh", "ash", "mksh":
		return true
	}
	return false
}

func replaceBlock(content, block string) string {
	start := strings.Index(content, beginMarker)
	end := strings.Index(content, endMarker)
	if start < 0 || end < start {
		return content
	}
	end += len(endMarker)
	if end < len(content) && content[end] == '\n' {
		end++
	}
	return content[:start] + block + content[end:]
}

func writeHook(path, content string, opts Options, verb string) error {
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return err
	}
	opts.say("%s in %s", verb, path)
	return nil
}

// hookFile returns the absolute path of the pre-commit hook, honouring
// core.hooksPath and linked worktrees.
func hookFile(g gitx.Git) (string, error) {
	inside, err := g.Line("rev-parse", "--is-inside-work-tree")
	if err != nil || inside != "true" {
		return "", errors.New("not inside a git work tree")
	}
	p, err := g.Line("rev-parse", "--path-format=absolute", "--git-path", "hooks/pre-commit")
	if err != nil {
		// git < 2.31: no --path-format; the relative answer is relative to
		// the current directory.
		if p, err = g.Line("rev-parse", "--git-path", "hooks/pre-commit"); err != nil {
			return "", err
		}
	}
	return filepath.Abs(filepath.FromSlash(p))
}

// manager detects hook managers that regenerate the hook file, and returns
// instructions for them instead.
func manager(g gitx.Git, hookPath string) string {
	hooksPath, _ := g.Line("config", "--get", "core.hooksPath")
	if strings.Contains(filepath.ToSlash(hooksPath), ".husky") {
		return "husky manages this repository's hooks; add this line to .husky/pre-commit instead:\n\n    deslop-hook hook\n"
	}
	content, err := os.ReadFile(hookPath)
	if err != nil {
		return ""
	}
	switch {
	case bytes.Contains(content, []byte("LEFTHOOK")) || bytes.Contains(content, []byte("lefthook")):
		return "lefthook manages this repository's hooks; add this to lefthook.yml instead:\n\n" +
			"    pre-commit:\n      commands:\n        deslop-hook:\n          run: deslop-hook hook\n"
	case bytes.Contains(content, []byte("File generated by pre-commit")):
		return "the pre-commit framework manages this repository's hooks; add this to .pre-commit-config.yaml instead:\n\n" +
			"    - repo: " + ProjectURL + "\n      rev: v0.1.0\n      hooks:\n        - id: deslop-hook\n"
	}
	return ""
}

// Installed reports whether the repository hook contains our block.
func Installed(g gitx.Git) bool {
	p, err := hookFile(g)
	if err != nil {
		return false
	}
	content, err := os.ReadFile(p)
	return err == nil && bytes.Contains(content, []byte(beginMarker))
}

// Uninstall removes the hook.
func Uninstall(g gitx.Git, opts Options) error {
	if opts.Global {
		return uninstallGlobal(g, opts)
	}
	hookPath, err := hookFile(g)
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(hookPath)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && !bytes.Contains(existing, []byte(beginMarker))) {
		opts.say("not installed in %s", hookPath)
		return nil
	}
	if err != nil {
		return err
	}
	rest := replaceBlock(string(existing), "")
	chain := hookPath + chainSuffix
	if strings.Contains(rest, filepath.Base(chain)) {
		if err := os.Rename(chain, hookPath); err != nil {
			return err
		}
		opts.say("removed; your original hook is back in %s", hookPath)
		return nil
	}
	if first, body, _ := strings.Cut(rest, "\n"); strings.HasPrefix(first, "#!") && strings.TrimSpace(body) == "" {
		if err := os.Remove(hookPath); err != nil {
			return err
		}
		opts.say("removed %s", hookPath)
		return nil
	}
	if err := os.WriteFile(hookPath, []byte(rest), 0o755); err != nil {
		return err
	}
	opts.say("removed from %s", hookPath)
	return nil
}

func installGlobal(g gitx.Git, opts Options) error {
	ma, mi, err := g.Version()
	if err != nil {
		return err
	}
	if ma < 2 || ma == 2 && mi < 54 {
		return fmt.Errorf("a global hook needs git 2.54 or newer (config-based hooks); you have %d.%d. Run `deslop-hook install` in each repository instead", ma, mi)
	}
	if _, err := g.Run(nil, "config", "--global", "--replace-all", "hook."+HookName+".event", "pre-commit"); err != nil {
		return err
	}
	if _, err := g.Run(nil, "config", "--global", "hook."+HookName+".command", GlobalCommand); err != nil {
		return err
	}
	opts.say("installed globally (hook.%s in ~/.gitconfig). It fixes repositories that opted in and only reports elsewhere; opt a repository out with `git config hook.%s.enabled false`", HookName, HookName)
	return nil
}

func uninstallGlobal(g gitx.Git, opts Options) error {
	if _, err := g.Run(nil, "config", "--global", "--remove-section", "hook."+HookName); err != nil {
		var ge *gitx.Error
		if errors.As(err, &ge) && strings.Contains(ge.Stderr, "no such section") {
			opts.say("not installed globally")
			return nil
		}
		return err
	}
	opts.say("removed the global hook")
	return nil
}

// GlobalActive reports whether the global config hook will run (or has
// run) for this commit, so the repository hook can stand down instead of
// running twice.
func GlobalActive(g gitx.Git) bool {
	if cmd, err := g.Line("config", "--get", "hook."+HookName+".command"); err != nil || cmd == "" {
		return false
	}
	if !Enabled(g) {
		return false
	}
	events, _ := g.Line("config", "--get-all", "hook."+HookName+".event")
	if !strings.Contains(events, "pre-commit") {
		return false
	}
	return g.AtLeast(2, 54)
}

// Enabled reports whether hook.deslop.enabled is not set to false.
func Enabled(g gitx.Git) bool {
	v, err := g.Line("config", "--type=bool", "--get", "hook."+HookName+".enabled")
	return err != nil || v != "false"
}
