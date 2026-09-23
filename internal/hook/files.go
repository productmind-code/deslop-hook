package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/productmind-code/deslop-hook/internal/config"
)

// files checks or fixes whole working-tree files: every tracked file with
// --all, or the given paths (directories expand to their tracked files; an
// untracked file named explicitly is included too).
func (r *Runner) files(opts Options) ([]FileResult, error) {
	var paths []string
	var results []FileResult
	if opts.All || len(opts.Paths) > 0 {
		var ask []string
		if !opts.All {
			ask = opts.Paths
		}
		entries, err := r.Git.LsFiles(ask)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, e := range entries {
			if seen[e.Path] {
				continue // unmerged entries appear once per stage
			}
			seen[e.Path] = true
			switch {
			case e.Stage != "0":
				results = append(results, FileResult{Path: e.Path, Skip: "has merge conflicts"})
			case !e.Regular():
				results = append(results, FileResult{Path: e.Path, Skip: "not a regular file"})
			case e.Tag == 'S':
				results = append(results, FileResult{Path: e.Path, Skip: "outside the sparse checkout"})
			default:
				paths = append(paths, e.Path)
			}
		}
		if !opts.All {
			for _, p := range opts.Paths {
				if seen[p] {
					continue
				}
				if fi, err := os.Lstat(filepath.FromSlash(p)); err == nil && fi.Mode().IsRegular() {
					paths = append(paths, p)
					seen[p] = true
				} else if err != nil && !hasPrefixDir(seen, p) {
					return nil, fmt.Errorf("%s: no such file", p)
				}
			}
		}
	}
	if len(paths) == 0 {
		return results, nil
	}
	sort.Strings(paths)

	attrs, err := r.Git.CheckAttr(paths, config.Attributes)
	if err != nil {
		return nil, err
	}
	write := opts.Mode == FixFiles && !opts.DryRun

	out := make([]FileResult, len(paths))
	var wg sync.WaitGroup
	work := make(chan int)
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				out[i] = r.oneFile(paths[i], attrs[paths[i]], write)
			}
		}()
	}
	for i := range paths {
		work <- i
	}
	close(work)
	wg.Wait()

	for _, fr := range out {
		if len(fr.Findings) > 0 || fr.Skip != "" || fr.Warning != "" {
			results = append(results, fr)
		}
	}
	return results, nil
}

func hasPrefixDir(seen map[string]bool, dir string) bool {
	prefix := dir + "/"
	for p := range seen {
		if len(p) > len(prefix) && p[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func (r *Runner) oneFile(p string, attrs map[string]string, write bool) FileResult {
	fr := FileResult{Path: p}
	opt, skip := r.Cfg.ForPath(p, attrs)
	if skip != "" {
		fr.Skip = skip
		return fr
	}
	path := filepath.FromSlash(p)
	fi, err := os.Lstat(path)
	if err != nil {
		fr.Skip = "missing from the working tree"
		return fr
	}
	if !fi.Mode().IsRegular() {
		fr.Skip = "not a regular file"
		return fr
	}
	if fi.Size() > r.Cfg.MaxFileSize {
		fr.Skip = fmt.Sprintf("larger than max_file_size (%d bytes)", r.Cfg.MaxFileSize)
		return fr
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fr.Warning = err.Error()
		return fr
	}
	if config.Binary(data) {
		fr.Skip = "binary"
		return fr
	}
	res := r.Scrub.File(data, nil, opt)
	if res.Invalid {
		fr.Skip = "not valid UTF-8"
		return fr
	}
	fr.Findings = res.Findings
	if write && len(res.Changed) > 0 {
		if err := writeAtomic(path, res.Out, fi.Mode().Perm()); err != nil {
			fr.Warning = fmt.Sprintf("not written: %v", err)
			return fr
		}
		fr.Written = true
	}
	return fr
}
