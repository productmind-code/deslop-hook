package hook

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/productmind-code/deslop-hook/internal/scrub"
)

const manualHint = "a straight quote on the same line means it may sit inside a string; fix it by hand, write it as an escape, or add deslop:ignore to the line"

// report prints the results and returns the exit code.
func (r *Runner) report(opts Options, files []FileResult) int {
	sort.SliceStable(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	if opts.Verbose {
		for _, f := range files {
			if f.Skip != "" {
				fmt.Fprintf(r.Stderr, "deslop-hook: skipped %s: %s\n", f.Path, f.Skip)
			}
		}
	}
	for _, f := range files {
		if f.Warning != "" {
			r.warnf("%s: %s", f.Path, f.Warning)
		}
	}

	findings, manual, nfiles := 0, 0, 0
	for _, f := range files {
		if len(f.Findings) > 0 {
			nfiles++
			findings += len(f.Findings)
			manual += f.manual()
		}
	}

	switch opts.Mode {
	case Hook, WorktreeOnly:
		r.summary(files, "cleaned")
		r.manual(files)
		return 0
	case FixFiles:
		if opts.DryRun {
			r.list(files, r.Stdout)
			fmt.Fprintf(r.Stderr, "deslop-hook: dry run: %d %s in %d %s would change\n", findings-manual, plural(findings-manual, "character", "characters"), nfiles, plural(nfiles, "file", "files"))
			return 0
		}
		r.summary(files, "fixed")
		r.manual(files)
		return 0
	case Report:
		if findings > 0 {
			fmt.Fprintf(r.Stderr, "deslop-hook: this repository has not opted in, so nothing was changed. It would clean:\n")
			r.list(files, r.Stderr)
			fmt.Fprintf(r.Stderr, "deslop-hook: to opt in, run `deslop-hook install` here or add a %s file\n", ".deslop-hook.toml")
		}
		return 0
	}

	// Check modes.
	if opts.Format == "github" {
		r.github(files)
	} else {
		r.list(files, r.Stdout)
	}
	if findings == 0 {
		return 0
	}
	fmt.Fprintf(r.Stderr, "deslop-hook: %d %s in %d %s\n", findings, plural(findings, "finding", "findings"), nfiles, plural(nfiles, "file", "files"))
	return 1
}

func describe(f scrub.Finding) string {
	head := fmt.Sprintf("U+%04X %s", f.Rune, f.Name)
	switch {
	case f.Manual:
		return head + " (" + manualHint + ")"
	case f.Repl == "":
		return head + " -> (removed)"
	default:
		return fmt.Sprintf("%s -> %q", head, f.Repl)
	}
}

func (r *Runner) list(files []FileResult, w io.Writer) {
	for _, f := range files {
		for _, x := range f.Findings {
			fmt.Fprintf(w, "%s:%d:%d: %s\n", f.Path, x.Line, x.Col, describe(x))
		}
	}
}

// summary prints one line per rewritten file, grouping characters by name.
func (r *Runner) summary(files []FileResult, verb string) {
	total, nfiles := 0, 0
	var lines []string
	width := 0
	for _, f := range files {
		if !f.Written || f.fixed() == 0 {
			continue
		}
		nfiles++
		total += f.fixed()
		if len(f.Path) > width {
			width = len(f.Path)
		}
	}
	if total == 0 {
		return
	}
	for _, f := range files {
		if !f.Written || f.fixed() == 0 {
			continue
		}
		counts := map[string]int{}
		var order []string
		for _, x := range f.Findings {
			if x.Manual {
				continue
			}
			if counts[x.Name] == 0 {
				order = append(order, x.Name)
			}
			counts[x.Name]++
		}
		var parts []string
		for _, n := range order {
			parts = append(parts, fmt.Sprintf("%s x%d", strings.ToLower(n), counts[n]))
		}
		lines = append(lines, fmt.Sprintf("  %-*s  %s", width, f.Path, strings.Join(parts, ", ")))
	}
	fmt.Fprintf(r.Stderr, "deslop-hook: %s %d %s in %d %s\n", verb, total, plural(total, "character", "characters"), nfiles, plural(nfiles, "file", "files"))
	for _, l := range lines {
		fmt.Fprintln(r.Stderr, l)
	}
}

func (r *Runner) manual(files []FileResult) {
	n := 0
	for _, f := range files {
		n += f.manual()
	}
	if n == 0 {
		return
	}
	fmt.Fprintf(r.Stderr, "deslop-hook: %d %s left for you to fix (%s):\n", n, plural(n, "character", "characters"), manualHint)
	for _, f := range files {
		for _, x := range f.Findings {
			if x.Manual {
				fmt.Fprintf(r.Stderr, "  %s:%d:%d %s\n", f.Path, x.Line, x.Col, x.Name)
			}
		}
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// github prints workflow-command annotations and, when running in Actions,
// a table in the step summary (annotations are capped per step).
func (r *Runner) github(files []FileResult) {
	for _, f := range files {
		for _, x := range f.Findings {
			fmt.Fprintf(r.Stdout, "::error file=%s,line=%d,col=%d,title=deslop-hook::%s\n",
				escapeProperty(f.Path), x.Line, x.Col, escapeData(describe(x)))
		}
	}
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return
	}
	var b strings.Builder
	b.WriteString("### deslop-hook\n\n")
	count := 0
	for _, f := range files {
		count += len(f.Findings)
	}
	if count == 0 {
		b.WriteString("No AI-slop characters in the added lines.\n")
	} else {
		fmt.Fprintf(&b, "%d %s in the added lines. Run `deslop-hook fix <file>` locally, or install the hook with `deslop-hook install`.\n\n", count, plural(count, "finding", "findings"))
		b.WriteString("| Location | Character | Becomes |\n| --- | --- | --- |\n")
		shown := 0
		for _, f := range files {
			for _, x := range f.Findings {
				if shown == 200 {
					fmt.Fprintf(&b, "| ... %d more | | |\n", count-shown)
					goto done
				}
				repl := fmt.Sprintf("`%q`", x.Repl)
				switch {
				case x.Manual:
					repl = "fix by hand"
				case x.Repl == "":
					repl = "(removed)"
				}
				fmt.Fprintf(&b, "| `%s:%d:%d` | U+%04X %s | %s |\n", f.Path, x.Line, x.Col, x.Rune, x.Name, repl)
				shown++
			}
		}
	}
done:
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		r.warnf("could not write the step summary: %v", err)
		return
	}
	defer fh.Close()
	fh.WriteString(b.String())
}

func escapeData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

func escapeProperty(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}
