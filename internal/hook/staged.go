package hook

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/productmind-code/deslop-hook/internal/config"
	"github.com/productmind-code/deslop-hook/internal/gitx"
	"github.com/productmind-code/deslop-hook/internal/scrub"
)

// diffArgs are the flags every diff call uses, chosen so that no user
// configuration (colour, external diff tools, textconv, mnemonic prefixes,
// relative paths, submodule summaries) can change the output format.
var diffArgs = []string{
	"-c", "core.quotePath=false",
	"--literal-pathspecs",
	"diff", "--no-color", "--no-ext-diff", "--no-textconv",
	"--src-prefix=a/", "--dst-prefix=b/", "--no-relative",
	"--ignore-submodules=all", "--diff-filter=ACMRT",
}

func (r *Runner) rawDiff(args ...string) ([]gitx.Change, error) {
	full := append(append([]string{}, diffArgs...), "--raw", "-z", "-M", "--no-abbrev")
	out, err := r.Git.Run(nil, append(full, args...)...)
	if err != nil {
		return nil, err
	}
	return gitx.ParseRaw(out)
}

func (r *Runner) hunks(args ...string) (map[string][]gitx.Hunk, error) {
	full := append(append([]string{}, diffArgs...), "-U0")
	out, err := r.Git.Run(nil, append(full, args...)...)
	if err != nil {
		return nil, err
	}
	return gitx.ParseUnified(out)
}

// hunksFor runs a -U0 diff limited to paths, in chunks.
func (r *Runner) hunksFor(paths []string, args ...string) (map[string][]gitx.Hunk, error) {
	all := map[string][]gitx.Hunk{}
	for _, chunk := range gitx.Chunks(paths) {
		h, err := r.hunks(append(append(append([]string{}, args...), "--"), chunk...)...)
		if err != nil {
			return nil, err
		}
		for p, hs := range h {
			all[p] = hs
		}
	}
	return all, nil
}

func addedSet(hunks []gitx.Hunk) *scrub.LineSet {
	var rs []scrub.Range
	for _, a := range gitx.AddedLines(hunks) {
		rs = append(rs, scrub.Range{Start: a[0], End: a[1]})
	}
	return scrub.NewLineSet(rs)
}

// target is one staged (or committed) file to scrub.
type target struct {
	change gitx.Change
	opt    scrub.Options
	lines  *scrub.LineSet
	orig   []byte
	res    scrub.Result
}

func (r *Runner) staged(opts Options) ([]FileResult, error) {
	changes, err := r.rawDiff("--cached")
	if err != nil {
		return nil, err
	}
	var only map[string]bool
	if len(opts.Paths) > 0 {
		only = map[string]bool{}
		for _, p := range opts.Paths {
			only[p] = true
		}
	}
	var results []FileResult
	var cands []gitx.Change
	for _, c := range changes {
		if only != nil && !only[c.Path] {
			continue
		}
		if !c.Regular() {
			results = append(results, FileResult{Path: c.Path, Skip: "not a regular file"})
			continue
		}
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return results, nil
	}

	// Sparse checkouts: an entry outside the cone has no working-tree file,
	// and rewriting it with update-index would drop its skip-worktree bit,
	// making git think the file was deleted.
	paths := make([]string, len(cands))
	for i, c := range cands {
		paths[i] = c.Path
	}
	entries, err := r.Git.LsFiles(paths)
	if err != nil {
		return nil, err
	}
	skipWT := map[string]bool{}
	for _, e := range entries {
		if e.Tag == 'S' {
			skipWT[e.Path] = true
		}
	}

	attrs, err := r.Git.CheckAttr(paths, config.Attributes, "--cached")
	if err != nil {
		return nil, err
	}

	// Lines added relative to HEAD. The whole staged diff, not a per-path
	// one: rename detection needs both sides, or a renamed file would count
	// as entirely new.
	added, err := r.hunks("--cached", "-M")
	if err != nil {
		return nil, err
	}
	// In a merge, keep only lines that are new relative to every parent:
	// the conflict resolutions, not the other branch's code.
	mergeHeads, err := r.mergeHeads()
	if err != nil {
		return nil, err
	}
	var mergeAdded []map[string][]gitx.Hunk
	for _, mh := range mergeHeads {
		h, err := r.hunks("--cached", "-M", mh)
		if err != nil {
			return nil, err
		}
		mergeAdded = append(mergeAdded, h)
	}

	var targets []*target
	for _, c := range cands {
		if skipWT[c.Path] {
			results = append(results, FileResult{Path: c.Path, Skip: "outside the sparse checkout"})
			continue
		}
		opt, skip := r.Cfg.ForPath(c.Path, attrs[c.Path])
		if skip != "" {
			results = append(results, FileResult{Path: c.Path, Skip: skip})
			continue
		}
		lines := addedSet(added[c.Path])
		for _, m := range mergeAdded {
			lines = lines.Intersect(addedSet(m[c.Path]))
		}
		if lines.Empty() {
			continue
		}
		targets = append(targets, &target{change: c, opt: opt, lines: lines})
	}

	byOID := map[string][]*target{}
	oids := []string{}
	for _, t := range targets {
		if _, ok := byOID[t.change.DstOID]; !ok {
			oids = append(oids, t.change.DstOID)
		}
		byOID[t.change.DstOID] = append(byOID[t.change.DstOID], t)
	}
	skipped := map[*target]string{}
	err = r.Git.ReadBlobs(oids, r.Cfg.MaxFileSize, func(b gitx.Blob) error {
		for _, t := range byOID[b.OID] {
			switch {
			case b.Missing:
				return fmt.Errorf("staged blob %s for %s is missing", b.OID, t.change.Path)
			case b.TooBig:
				skipped[t] = fmt.Sprintf("larger than max_file_size (%d bytes)", r.Cfg.MaxFileSize)
			case config.Binary(b.Data):
				skipped[t] = "binary"
			default:
				t.orig = b.Data
				t.res = r.Scrub.File(b.Data, t.lines, t.opt)
				if t.res.Invalid {
					skipped[t] = "not valid UTF-8"
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	var changed []*target
	for _, t := range targets {
		if why, ok := skipped[t]; ok {
			results = append(results, FileResult{Path: t.change.Path, Skip: why})
			continue
		}
		if len(t.res.Changed) > 0 {
			changed = append(changed, t)
		}
	}

	if !opts.Mode.writesIndex() && !opts.Mode.writesWorktree() || len(changed) == 0 {
		for _, t := range targets {
			if _, ok := skipped[t]; !ok && len(t.res.Findings) > 0 {
				results = append(results, FileResult{Path: t.change.Path, Findings: t.res.Findings})
			}
		}
		return results, nil
	}

	// Map index lines to working-tree lines before anything is written;
	// afterwards every scrubbed line would look like an unstaged change.
	changedPaths := make([]string, len(changed))
	for i, t := range changed {
		changedPaths[i] = t.change.Path
	}
	wtHunks, err := r.hunksFor(changedPaths)
	if err != nil {
		return nil, err
	}

	written := map[*target]bool{}
	if opts.Mode.writesIndex() {
		contents := make([][]byte, len(changed))
		for i, t := range changed {
			contents[i] = t.res.Out
		}
		oids, err := r.Git.HashBlobs(contents)
		if err != nil {
			return nil, err
		}
		updates := make([]gitx.IndexUpdate, len(changed))
		for i, t := range changed {
			updates[i] = gitx.IndexUpdate{Mode: t.change.DstMode, OID: oids[i], Path: t.change.Path}
		}
		if err := r.updateIndex(r.Git, updates); err != nil {
			return nil, err
		}
		if lock := r.partialCommitIndex(); lock != "" {
			if err := r.updateIndex(r.Git.With("GIT_INDEX_FILE="+lock), updates); err != nil {
				r.warnf("the commit is clean, but the index could not be updated to match (%v); run `git status` after committing", err)
			}
		}
		for _, t := range changed {
			written[t] = true
		}
	}

	wt := &worktree{r: r}
	var frs []*FileResult
	for _, t := range targets {
		if _, ok := skipped[t]; ok {
			continue
		}
		fr := &FileResult{Path: t.change.Path, Findings: t.res.Findings, Written: written[t]}
		frs = append(frs, fr)
		if len(t.res.Changed) > 0 {
			wt.plan(t, wtHunks[t.change.Path], fr)
		}
	}
	wt.write()
	if err := wt.saveLog(); err != nil {
		r.warnf("could not write the backup log: %v", err)
	}
	for _, fr := range frs {
		if len(fr.Findings) > 0 || fr.Warning != "" {
			results = append(results, *fr)
		}
	}
	return results, nil
}

// mergeHeads returns the commits being merged, if a merge is in progress.
func (r *Runner) mergeHeads() ([]string, error) {
	p, err := r.Git.Line("rev-parse", "--git-path", "MERGE_HEAD")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var heads []string
	for _, l := range strings.Fields(string(data)) {
		heads = append(heads, l)
	}
	return heads, nil
}

// updateIndex applies updates, waiting briefly if another process (an IDE's
// git integration, say) holds the index lock. It fails closed: a commit must
// not go through half-scrubbed.
func (r *Runner) updateIndex(g gitx.Git, updates []gitx.IndexUpdate) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		if err = g.UpdateIndex(updates); err == nil {
			return nil
		}
		if !strings.Contains(err.Error(), ".lock") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("updating the index: %w (to commit without deslop-hook, set DESLOP_SKIP=1)", err)
}

// base checks the lines added between opts.Base and HEAD, reading committed
// content. This is the CI check.
func (r *Runner) base(opts Options) ([]FileResult, error) {
	spec := opts.Base + "...HEAD"
	changes, err := r.rawDiff(spec)
	if err != nil {
		return nil, err
	}
	added, err := r.hunks("-M", spec)
	if err != nil {
		return nil, err
	}
	var results []FileResult
	var paths []string
	var cands []gitx.Change
	for _, c := range changes {
		if !c.Regular() {
			continue
		}
		cands = append(cands, c)
		paths = append(paths, c.Path)
	}
	attrs, err := r.Git.CheckAttr(paths, config.Attributes)
	if err != nil {
		return nil, err
	}
	byOID := map[string][]*target{}
	var oids []string
	for _, c := range cands {
		opt, skip := r.Cfg.ForPath(c.Path, attrs[c.Path])
		if skip != "" {
			results = append(results, FileResult{Path: c.Path, Skip: skip})
			continue
		}
		lines := addedSet(added[c.Path])
		if lines.Empty() {
			continue
		}
		if _, ok := byOID[c.DstOID]; !ok {
			oids = append(oids, c.DstOID)
		}
		byOID[c.DstOID] = append(byOID[c.DstOID], &target{change: c, opt: opt, lines: lines})
	}
	err = r.Git.ReadBlobs(oids, r.Cfg.MaxFileSize, func(b gitx.Blob) error {
		for _, t := range byOID[b.OID] {
			switch {
			case b.Missing:
				return fmt.Errorf("blob %s for %s is missing (is the clone deep enough?)", b.OID, t.change.Path)
			case b.TooBig:
				results = append(results, FileResult{Path: t.change.Path, Skip: "larger than max_file_size"})
			case config.Binary(b.Data):
				results = append(results, FileResult{Path: t.change.Path, Skip: "binary"})
			default:
				res := r.Scrub.File(b.Data, t.lines, t.opt)
				if res.Invalid {
					results = append(results, FileResult{Path: t.change.Path, Skip: "not valid UTF-8"})
				} else if len(res.Findings) > 0 {
					results = append(results, FileResult{Path: t.change.Path, Findings: res.Findings})
				}
			}
		}
		return nil
	})
	return results, err
}

// splitLines returns the [start, end) byte offsets of each line, without
// its '\n'.
func splitLines(data []byte) [][2]int {
	var out [][2]int
	for start := 0; start < len(data); {
		i := bytes.IndexByte(data[start:], '\n')
		if i < 0 {
			out = append(out, [2]int{start, len(data)})
			break
		}
		out = append(out, [2]int{start, start + i})
		start += i + 1
	}
	return out
}
