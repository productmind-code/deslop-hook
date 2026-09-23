// Package gitx wraps the handful of git plumbing commands deslop-hook needs.
// Every call goes through the git binary, so the tool behaves exactly as git
// does for the user: worktrees, sparse checkouts, SHA-256 repositories,
// GIT_INDEX_FILE during a commit, and so on.
package gitx

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Git runs git commands. Env entries are appended to the process
// environment and win over it; Unset names variables to drop from it.
type Git struct {
	Env   []string
	Unset []string
}

// Error is a failed git command, with its stderr.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *Error) Unwrap() error { return e.Err }

func (g Git) command(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	if len(g.Env) > 0 || len(g.Unset) > 0 {
		env := os.Environ()
		if len(g.Unset) > 0 {
			kept := env[:0:0]
			for _, kv := range env {
				drop := false
				for _, k := range g.Unset {
					if strings.HasPrefix(kv, k+"=") {
						drop = true
						break
					}
				}
				if !drop {
					kept = append(kept, kv)
				}
			}
			env = kept
		}
		cmd.Env = append(env, g.Env...)
	}
	return cmd
}

// With returns a copy of g with extra environment entries.
func (g Git) With(env ...string) Git {
	return Git{Env: append(append([]string(nil), g.Env...), env...), Unset: g.Unset}
}

// Without returns a copy of g that drops the named variables.
func (g Git) Without(keys ...string) Git {
	return Git{Env: g.Env, Unset: append(append([]string(nil), g.Unset...), keys...)}
}

// Run runs git with stdin (may be nil) and returns stdout.
func (g Git) Run(stdin []byte, args ...string) ([]byte, error) {
	cmd := g.command(args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return stdout.Bytes(), nil
}

// Line runs git and returns stdout with the trailing newline trimmed.
func (g Git) Line(args ...string) (string, error) {
	out, err := g.Run(nil, args...)
	return strings.TrimRight(string(out), "\r\n"), err
}

// Version returns git's major and minor version.
func (g Git) Version() (major, minor int, err error) {
	out, err := g.Line("version")
	if err != nil {
		return 0, 0, err
	}
	return ParseVersion(out)
}

// ParseVersion parses `git version` output such as "git version 2.45.1",
// "git version 2.39.3 (Apple Git-146)" or "git version 2.45.1.windows.1".
func ParseVersion(s string) (major, minor int, err error) {
	f := strings.Fields(s)
	if len(f) < 3 {
		return 0, 0, fmt.Errorf("unexpected git version output %q", s)
	}
	parts := strings.SplitN(f[2], ".", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("unexpected git version %q", f[2])
	}
	if major, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, fmt.Errorf("unexpected git version %q", f[2])
	}
	if minor, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, fmt.Errorf("unexpected git version %q", f[2])
	}
	return major, minor, nil
}

// AtLeast reports whether git is at least major.minor.
func (g Git) AtLeast(major, minor int) bool {
	ma, mi, err := g.Version()
	return err == nil && (ma > major || ma == major && mi >= minor)
}

// Change is one entry of `git diff --raw -z`.
type Change struct {
	SrcMode, DstMode string
	SrcOID, DstOID   string
	Status           byte // A, C, M, R, T
	Path             string
	OldPath          string // renames and copies only
}

// Regular reports whether the destination is a regular file (not a symlink
// or a submodule).
func (c Change) Regular() bool { return c.DstMode == "100644" || c.DstMode == "100755" }

// ParseRaw parses `git diff --raw -z` output.
func ParseRaw(out []byte) ([]Change, error) {
	var changes []Change
	fields := bytes.Split(out, []byte{0})
	for i := 0; i < len(fields); i++ {
		head := string(fields[i])
		if head == "" {
			continue
		}
		if head[0] != ':' {
			return nil, fmt.Errorf("unexpected raw diff record %q", head)
		}
		f := strings.Fields(head[1:])
		if len(f) != 5 || f[4] == "" {
			return nil, fmt.Errorf("unexpected raw diff record %q", head)
		}
		c := Change{SrcMode: f[0], DstMode: f[1], SrcOID: f[2], DstOID: f[3], Status: f[4][0]}
		if i+1 >= len(fields) {
			return nil, errors.New("truncated raw diff")
		}
		i++
		c.Path = string(fields[i])
		if c.Status == 'R' || c.Status == 'C' {
			if i+1 >= len(fields) {
				return nil, errors.New("truncated raw diff")
			}
			i++
			c.OldPath, c.Path = c.Path, string(fields[i])
		}
		changes = append(changes, c)
	}
	return changes, nil
}

// Hunk is one `@@ -a,b +c,d @@` header.
type Hunk struct {
	OldStart, OldCount int
	NewStart, NewCount int
}

// ParseUnified parses unified diff output made with a/ b/ prefixes and
// returns the hunks of each file, keyed by the new path. Content lines are
// consumed by count, never by their first character, so a file line that
// starts with "++ " cannot be mistaken for a header.
func ParseUnified(out []byte) (map[string][]Hunk, error) {
	files := map[string][]Hunk{}
	var cur string
	haveFile := false
	remaining := 0
	for len(out) > 0 {
		var line []byte
		if i := bytes.IndexByte(out, '\n'); i >= 0 {
			line, out = out[:i], out[i+1:]
		} else {
			line, out = out, nil
		}
		if remaining > 0 {
			if len(line) > 0 && line[0] == '\\' {
				continue // "\ No newline at end of file"
			}
			remaining--
			continue
		}
		switch {
		case bytes.HasPrefix(line, []byte("diff --git ")):
			haveFile = false
		case bytes.HasPrefix(line, []byte("+++ ")):
			p := string(line[4:])
			if p == "/dev/null" {
				haveFile = false
				continue
			}
			p, err := unquoteC(p)
			if err != nil {
				return nil, err
			}
			if !strings.HasPrefix(p, "b/") {
				return nil, fmt.Errorf("unexpected diff path %q", p)
			}
			cur, haveFile = p[2:], true
			if _, ok := files[cur]; !ok {
				files[cur] = nil
			}
		case bytes.HasPrefix(line, []byte("@@ ")):
			h, err := parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			if haveFile {
				files[cur] = append(files[cur], h)
			}
			remaining = h.OldCount + h.NewCount
		case len(line) > 0 && line[0] == '\\':
			// "\ No newline at end of file" after a hunk that ended exactly.
		}
	}
	return files, nil
}

func parseHunkHeader(line []byte) (Hunk, error) {
	// @@ -a[,b] +c[,d] @@ optional section heading
	s := string(line)
	f := strings.Fields(s)
	if len(f) < 4 || f[0] != "@@" || f[3] != "@@" || !strings.HasPrefix(f[1], "-") || !strings.HasPrefix(f[2], "+") {
		return Hunk{}, fmt.Errorf("unexpected hunk header %q", s)
	}
	var h Hunk
	var err error
	if h.OldStart, h.OldCount, err = parseRange(f[1][1:]); err != nil {
		return Hunk{}, fmt.Errorf("hunk header %q: %w", s, err)
	}
	if h.NewStart, h.NewCount, err = parseRange(f[2][1:]); err != nil {
		return Hunk{}, fmt.Errorf("hunk header %q: %w", s, err)
	}
	return h, nil
}

func parseRange(s string) (start, count int, err error) {
	count = 1
	if i := strings.IndexByte(s, ','); i >= 0 {
		if count, err = strconv.Atoi(s[i+1:]); err != nil {
			return 0, 0, err
		}
		s = s[:i]
	}
	start, err = strconv.Atoi(s)
	return start, count, err
}

// unquoteC undoes git's C-style path quoting ("a\tb", octal escapes).
func unquoteC(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s, nil
	}
	s = s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(s) {
			return "", fmt.Errorf("bad quoted path %q", s)
		}
		switch c = s[i]; c {
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case '"', '\\':
			b.WriteByte(c)
		default:
			if c < '0' || c > '3' || i+2 >= len(s) {
				return "", fmt.Errorf("bad quoted path %q", s)
			}
			n, err := strconv.ParseUint(s[i:i+3], 8, 8)
			if err != nil {
				return "", fmt.Errorf("bad quoted path %q", s)
			}
			b.WriteByte(byte(n))
			i += 2
		}
	}
	return b.String(), nil
}

// AddedLines turns hunks into the 1-based line numbers they add on the new
// side.
func AddedLines(hunks []Hunk) [][2]int {
	var out [][2]int
	for _, h := range hunks {
		if h.NewCount > 0 {
			out = append(out, [2]int{h.NewStart, h.NewStart + h.NewCount - 1})
		}
	}
	return out
}

// MapLine maps a line number on the old side of a -U0 diff to the new side.
// It returns 0 when the line was changed or removed.
func MapLine(hunks []Hunk, n int) int {
	offset := 0
	for _, h := range hunks {
		if h.OldCount == 0 {
			// Pure insertion after line OldStart.
			if h.OldStart < n {
				offset += h.NewCount
				continue
			}
			break
		}
		if n < h.OldStart {
			break
		}
		if n <= h.OldStart+h.OldCount-1 {
			return 0
		}
		offset += h.NewCount - h.OldCount
	}
	return n + offset
}

// Attrs maps path -> attribute -> value, where value is "set", "unset",
// "unspecified" or the attribute's string value.
type Attrs map[string]map[string]string

// CheckAttr queries attributes for many paths in one process. extra holds
// flags such as "--cached".
func (g Git) CheckAttr(paths, attrs []string, extra ...string) (Attrs, error) {
	res := Attrs{}
	if len(paths) == 0 {
		return res, nil
	}
	var in bytes.Buffer
	for _, p := range paths {
		in.WriteString(p)
		in.WriteByte(0)
	}
	// With --stdin the attribute names are the only arguments.
	args := append([]string{"check-attr", "-z", "--stdin"}, extra...)
	args = append(args, attrs...)
	out, err := g.Run(in.Bytes(), args...)
	if err != nil {
		return nil, err
	}
	f := bytes.Split(out, []byte{0})
	for i := 0; i+2 < len(f); i += 3 {
		p, a, v := string(f[i]), string(f[i+1]), string(f[i+2])
		m := res[p]
		if m == nil {
			m = map[string]string{}
			res[p] = m
		}
		m[a] = v
	}
	return res, nil
}

// IndexEntry is one line of `git ls-files -s -t -z`.
type IndexEntry struct {
	Tag   byte // H cached, S skip-worktree, ...
	Mode  string
	OID   string
	Stage string
	Path  string
}

// Regular reports whether the entry is a regular file.
func (e IndexEntry) Regular() bool { return e.Mode == "100644" || e.Mode == "100755" }

// LsFiles lists index entries. With no paths it lists the whole index.
func (g Git) LsFiles(paths []string) ([]IndexEntry, error) {
	var entries []IndexEntry
	run := func(ps []string) error {
		args := []string{"--literal-pathspecs", "ls-files", "-s", "-t", "-z", "--"}
		out, err := g.Run(nil, append(args, ps...)...)
		if err != nil {
			return err
		}
		for _, rec := range bytes.Split(out, []byte{0}) {
			if len(rec) == 0 {
				continue
			}
			tab := bytes.IndexByte(rec, '\t')
			if tab < 0 || len(rec) < 2 {
				return fmt.Errorf("unexpected ls-files record %q", rec)
			}
			f := strings.Fields(string(rec[2:tab]))
			if len(f) != 3 {
				return fmt.Errorf("unexpected ls-files record %q", rec)
			}
			entries = append(entries, IndexEntry{Tag: rec[0], Mode: f[0], OID: f[1], Stage: f[2], Path: string(rec[tab+1:])})
		}
		return nil
	}
	if len(paths) == 0 {
		return entries, run(nil)
	}
	for _, chunk := range Chunks(paths) {
		if err := run(chunk); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// Chunks splits paths so each command line stays well under the Windows
// 32K limit.
func Chunks(paths []string) [][]string {
	const budget = 16 << 10
	var out [][]string
	var cur []string
	size := 0
	for _, p := range paths {
		if size+len(p)+1 > budget && len(cur) > 0 {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, p)
		size += len(p) + 1
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// HashObject writes data as a blob, byte for byte (no clean filters), and
// returns its id.
func (g Git) HashObject(data []byte) (string, error) {
	return lineOf(g.Run(data, "hash-object", "-w", "--stdin", "--no-filters"))
}

// HashFiles writes the given files as blobs, byte for byte, in one process
// and returns their ids in order.
func (g Git) HashFiles(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	var in bytes.Buffer
	for _, p := range paths {
		if strings.ContainsAny(p, "\n\r") {
			return nil, fmt.Errorf("cannot hash %q in a batch", p)
		}
		in.WriteString(p + "\n")
	}
	out, err := g.Run(in.Bytes(), "hash-object", "-w", "--no-filters", "--stdin-paths")
	if err != nil {
		return nil, err
	}
	oids := strings.Fields(string(out))
	if len(oids) != len(paths) {
		return nil, fmt.Errorf("hash-object returned %d ids for %d files", len(oids), len(paths))
	}
	return oids, nil
}

// HashBlobs writes each content as a blob in one process (through
// temporary files) and returns the ids in order.
func (g Git) HashBlobs(contents [][]byte) ([]string, error) {
	if len(contents) == 0 {
		return nil, nil
	}
	if len(contents) == 1 {
		oid, err := g.HashObject(contents[0])
		return []string{oid}, err
	}
	dir, err := os.MkdirTemp("", "deslop-hook-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	paths := make([]string, len(contents))
	for i, c := range contents {
		paths[i] = filepath.Join(dir, strconv.Itoa(i))
		if err := os.WriteFile(paths[i], c, 0o600); err != nil {
			return nil, err
		}
	}
	return g.HashFiles(paths)
}

func lineOf(out []byte, err error) (string, error) {
	return strings.TrimSpace(string(out)), err
}

// IndexUpdate is one entry for UpdateIndex.
type IndexUpdate struct {
	Mode, OID, Path string
}

// UpdateIndex applies entries with `update-index -z --index-info` in one
// process.
func (g Git) UpdateIndex(updates []IndexUpdate) error {
	var in bytes.Buffer
	for _, u := range updates {
		fmt.Fprintf(&in, "%s %s\t%s", u.Mode, u.OID, u.Path)
		in.WriteByte(0)
	}
	_, err := g.Run(in.Bytes(), "update-index", "-z", "--index-info")
	return err
}

// Blob is one object read by ReadBlobs.
type Blob struct {
	OID     string
	Data    []byte // nil when TooBig or Missing
	Size    int64
	TooBig  bool
	Missing bool
}

// ReadBlobs streams objects through a single `git cat-file --batch`. Objects
// larger than maxSize are reported but not loaded.
func (g Git) ReadBlobs(oids []string, maxSize int64, fn func(Blob) error) error {
	if len(oids) == 0 {
		return nil
	}
	cmd := g.command("cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	writeErr := make(chan error, 1)
	go func() {
		w := bufio.NewWriter(stdin)
		for _, oid := range oids {
			if _, err := w.WriteString(oid + "\n"); err != nil {
				writeErr <- err
				stdin.Close()
				return
			}
		}
		err := w.Flush()
		stdin.Close()
		writeErr <- err
	}()

	r := bufio.NewReaderSize(stdout, 64<<10)
	var readErr error
	// Answers come back in request order; report each under the id it was
	// asked for (which may be abbreviated), not the full id git prints.
	for _, want := range oids {
		header, err := r.ReadString('\n')
		if err != nil {
			readErr = fmt.Errorf("cat-file: %w", err)
			break
		}
		f := strings.Fields(header)
		if len(f) == 2 && f[1] == "missing" {
			if readErr = fn(Blob{OID: want, Missing: true}); readErr != nil {
				break
			}
			continue
		}
		if len(f) != 3 {
			readErr = fmt.Errorf("cat-file: unexpected header %q", header)
			break
		}
		size, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			readErr = fmt.Errorf("cat-file: unexpected header %q", header)
			break
		}
		b := Blob{OID: want, Size: size}
		if size > maxSize {
			b.TooBig = true
			if _, err := io.CopyN(io.Discard, r, size+1); err != nil {
				readErr = fmt.Errorf("cat-file: %w", err)
				break
			}
		} else {
			b.Data = make([]byte, size+1)
			if _, err := io.ReadFull(r, b.Data); err != nil {
				readErr = fmt.Errorf("cat-file: %w", err)
				break
			}
			b.Data = b.Data[:size] // drop the trailing newline
		}
		if readErr = fn(b); readErr != nil {
			break
		}
	}
	if readErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return readErr
	}
	if err := <-writeErr; err != nil {
		_ = cmd.Wait()
		return fmt.Errorf("cat-file: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return &Error{Args: []string{"cat-file", "--batch"}, Stderr: stderr.String(), Err: err}
	}
	return nil
}
