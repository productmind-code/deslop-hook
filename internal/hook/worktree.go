package hook

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/productmind-code/deslop-hook/internal/gitx"
)

// worktree applies index-side scrubs to working-tree files without touching
// unstaged edits, and keeps a backup of every file it rewrites.
type worktree struct {
	r       *Runner
	pending []*pendingWrite
	backup  []string // "oid path" lines for the log
}

type pendingWrite struct {
	path string // OS path, relative to the top of the work tree
	slug string // repository path, for the log
	fi   fs.FileInfo
	out  []byte
	fr   *FileResult
}

// plan works out the new working-tree content for t. hunks is the
// index->worktree -U0 diff for the file, taken before the index was changed.
// A line is rewritten only when it is outside every unstaged hunk and still
// equal to the index line (ignoring a CR, for autocrlf), so a partially
// staged file keeps its unstaged changes exactly as they were.
func (w *worktree) plan(t *target, hunks []gitx.Hunk, fr *FileResult) {
	path := filepath.FromSlash(t.change.Path)
	fi, err := os.Lstat(path)
	if err != nil {
		return // deleted after staging: never recreate it
	}
	if !fi.Mode().IsRegular() {
		fr.Warning = "working-tree copy is not a regular file; only the commit was cleaned"
		return
	}
	if !w.inside(path) {
		fr.Warning = "working-tree path goes through a symlink; only the commit was cleaned"
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fr.Warning = fmt.Sprintf("working-tree copy not updated: %v", err)
		return
	}

	idx := splitLines(t.orig)
	wt := splitLines(data)
	repl := map[int][]byte{}
	missed := 0
	for _, n := range t.res.Changed {
		m := gitx.MapLine(hunks, n)
		if m < 1 || m > len(wt) || n > len(idx) {
			missed++
			continue
		}
		wl := data[wt[m-1][0]:wt[m-1][1]]
		il := t.orig[idx[n-1][0]:idx[n-1][1]]
		if !bytes.Equal(bytes.TrimSuffix(wl, []byte("\r")), bytes.TrimSuffix(il, []byte("\r"))) {
			missed++
			continue
		}
		out, _ := w.r.Scrub.Line(wl, t.opt, m == 1, m, nil)
		if !sameBytes(out, wl) {
			repl[m] = out
		}
	}
	if missed > 0 {
		fr.Warning = fmt.Sprintf("%d cleaned line(s) also have unstaged edits; the working-tree copy of those lines was left alone", missed)
	}
	if len(repl) == 0 {
		return
	}

	var out bytes.Buffer
	out.Grow(len(data) + 64)
	for i, span := range wt {
		if nl, ok := repl[i+1]; ok {
			out.Write(nl)
		} else {
			out.Write(data[span[0]:span[1]])
		}
		if span[1] < len(data) {
			out.WriteByte('\n')
		}
	}
	w.pending = append(w.pending, &pendingWrite{path: path, slug: t.change.Path, fi: fi, out: out.Bytes(), fr: fr})
}

// write backs up every planned file in one git process, then rewrites them.
func (w *worktree) write() {
	if len(w.pending) == 0 {
		return
	}
	paths := make([]string, len(w.pending))
	for i, p := range w.pending {
		paths[i] = p.path
	}
	oids, err := w.r.Git.HashFiles(paths)
	if err != nil {
		// Fall back to one process per file (e.g. a newline in a name).
		oids = make([]string, len(w.pending))
		for i, p := range w.pending {
			data, rerr := os.ReadFile(p.path)
			if rerr == nil {
				oids[i], rerr = w.r.Git.HashObject(data)
			}
			if rerr != nil {
				oids[i] = ""
			}
		}
	}
	for i, p := range w.pending {
		if oids[i] == "" {
			p.fr.Warning = "working-tree copy not updated: could not back it up"
			continue
		}
		w.backup = append(w.backup, oids[i]+" "+p.slug)
		// An editor or agent may be writing the file right now.
		if now, err := os.Lstat(p.path); err != nil || now.Size() != p.fi.Size() || !now.ModTime().Equal(p.fi.ModTime()) {
			p.fr.Warning = "working-tree copy changed while the hook ran; only the commit was cleaned"
			continue
		}
		if err := writeAtomic(p.path, p.out, p.fi.Mode().Perm()); err != nil {
			p.fr.Warning = fmt.Sprintf("working-tree copy not updated: %v", err)
			continue
		}
		p.fr.Written = true
	}
}

// inside reports whether path resolves inside the work tree without going
// through a symlinked directory.
func (w *worktree) inside(path string) bool {
	top, err := filepath.EvalSymlinks(w.r.Top)
	if err != nil {
		return false
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(filepath.Join(w.r.Top, path)))
	if err != nil {
		return false
	}
	want := filepath.Join(top, filepath.Dir(path))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(dir, want)
	}
	return dir == want
}

// saveLog records the pre-rewrite content of every file changed in this run,
// so an unstaged edit can always be recovered with `git cat-file -p <oid>`.
func (w *worktree) saveLog() error {
	if len(w.backup) == 0 {
		return nil
	}
	p, err := w.r.Git.Line("rev-parse", "--git-path", "deslop-hook/last-run")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# deslop-hook %s: working-tree content before the rewrite.\n", time.Now().Format(time.RFC3339))
	b.WriteString("# Restore a file with: git cat-file -p <oid> > <path>\n")
	for _, l := range w.backup {
		b.WriteString(l + "\n")
	}
	return os.WriteFile(p, []byte(b.String()), 0o644)
}

func sameBytes(a, b []byte) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// writeAtomic replaces path with data via a temporary file and a rename, so
// a crash never leaves a half-written file.
func writeAtomic(path string, data []byte, perm fs.FileMode) error {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, "."+base+".deslop-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, perm)
	}
	if werr != nil {
		os.Remove(name)
		return werr
	}
	var rerr error
	for attempt := 0; attempt < 5; attempt++ {
		if rerr = os.Rename(name, path); rerr == nil {
			return nil
		}
		if runtime.GOOS != "windows" {
			break
		}
		time.Sleep(50 * time.Millisecond) // an editor or scanner may hold the file
	}
	os.Remove(name)
	if runtime.GOOS == "windows" {
		// Last resort on Windows: rewrite in place.
		return os.WriteFile(path, data, perm)
	}
	return rerr
}
