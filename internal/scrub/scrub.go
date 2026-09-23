// Package scrub rewrites the Unicode characters that language models leave
// in text into what a person would type on a keyboard.
//
// The transform works one line at a time and never looks past a line
// boundary, so the same line gives the same result in the index (LF) and in
// a CRLF working tree, and running it twice changes nothing the second time.
package scrub

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"unicode"
	"unicode/utf8"
)

// Scrubber holds the effective character table: the built-in rules with any
// configuration overrides applied. It is safe for concurrent use.
type Scrubber struct {
	rules map[rune]Rule
}

// New builds a Scrubber. overrides maps a character to Keep (leave it
// alone) or to a replacement string ("" deletes it). A character that is not
// in the built-in table joins the Symbols group.
func New(overrides map[rune]string) *Scrubber {
	rules := make(map[rune]Rule, len(defaultRules)+len(overrides))
	for r, ru := range defaultRules {
		rules[r] = ru
	}
	for r, v := range overrides {
		ru, ok := rules[r]
		if !ok {
			ru = Rule{Group: Symbols, Name: fmt.Sprintf("U+%04X", r)}
		}
		if v == Keep {
			ru.kind = kindKeep
		} else {
			ru.kind, ru.repl = kindReplace, v
		}
		rules[r] = ru
	}
	return &Scrubber{rules: rules}
}

// Options control one file.
type Options struct {
	// Groups is the set of character families to rewrite.
	Groups Group
	// Prose turns quote safety off. In code, a curly quote on a line that
	// already holds the matching straight quote is probably inside a string
	// literal, where straightening it would end the string early.
	Prose bool
}

// Finding is one character that was (or, in check mode, would be) rewritten,
// or that needs a human.
type Finding struct {
	Line   int    // 1-based
	Col    int    // 1-based, in characters
	Rune   rune   // the character found
	Name   string // its Unicode name
	Repl   string // what it becomes; "" when it is deleted
	Manual bool   // left in place: quote safety applied
}

// Line scrubs one line: no trailing '\n', and a trailing '\r' is kept and
// treated as whitespace. fileStart reports whether the line begins at the
// first byte of the file (a byte order mark is legitimate there). It returns
// line itself, unmodified, when there is nothing to change, and appends to
// findings.
func (s *Scrubber) Line(line []byte, opt Options, fileStart bool, lineNo int, findings []Finding) ([]byte, []Finding) {
	first := firstNonASCII(line)
	if first < 0 || !utf8.Valid(line[first:]) {
		// Invalid UTF-8 is left alone: deleting a character between two
		// broken fragments could join them into a new, valid one.
		return line, findings
	}
	hasSingle := !opt.Prose && bytes.IndexByte(line, '\'') >= 0
	hasDouble := !opt.Prose && bytes.IndexByte(line, '"') >= 0

	var out []byte // nil until the first change
	prev := ' '    // the last rune emitted; line start counts as space
	if first > 0 {
		prev = rune(line[first-1])
	}
	col := first + 1 // everything before first is ASCII: one byte, one column
	i := first
	for i < len(line) {
		b := line[i]
		if b < utf8.RuneSelf {
			if out != nil {
				out = append(out, b)
			}
			prev = rune(b)
			i++
			col++
			continue
		}
		r, size := utf8.DecodeRune(line[i:])
		ru, ok := s.rules[r]
		if !ok || ru.Group&opt.Groups == 0 || ru.kind == kindKeep {
			if out != nil {
				out = append(out, line[i:i+size]...)
			}
			prev = r
			i += size
			col++
			continue
		}

		repl, keep, manual := "", false, false
		switch ru.kind {
		case kindDelete:
		case kindSpace, kindReplace:
			repl = ru.repl
		case kindBOM:
			keep = fileStart && i == 0
		case kindJoiner:
			next := s.nextSurviving(line, i+size, opt, hasSingle, hasDouble)
			keep = prev >= utf8.RuneSelf && next >= utf8.RuneSelf
		case kindEmDash:
			next := s.nextSurviving(line, i+size, opt, hasSingle, hasDouble)
			repl = "-"
			if isWord(prev) && isWord(next) {
				repl = " - "
			}
		case kindSingleQuote:
			keep, manual, repl = hasSingle, hasSingle, ru.repl
		case kindDoubleQuote:
			keep, manual, repl = hasDouble, hasDouble, ru.repl
		}

		if keep {
			if manual {
				findings = append(findings, Finding{Line: lineNo, Col: col, Rune: r, Name: ru.Name, Repl: repl, Manual: true})
			}
			if out != nil {
				out = append(out, line[i:i+size]...)
			}
			prev = r
			i += size
			col++
			continue
		}

		findings = append(findings, Finding{Line: lineNo, Col: col, Rune: r, Name: ru.Name, Repl: repl})
		if out == nil {
			out = make([]byte, i, len(line)+8)
			copy(out, line[:i])
		}
		out = append(out, repl...)
		if repl != "" {
			prev, _ = utf8.DecodeLastRuneInString(repl)
		}
		i += size
		col++
	}
	if out == nil {
		return line, findings
	}
	return out, findings
}

// nextSurviving returns the first rune at or after j as it will appear in
// the output, skipping characters that will be deleted. Line end and '\r'
// count as space. Neighbour rules look at output, not input, which is what
// makes the transform idempotent.
func (s *Scrubber) nextSurviving(line []byte, j int, opt Options, hasSingle, hasDouble bool) rune {
	for j < len(line) {
		b := line[j]
		if b < utf8.RuneSelf {
			if b == '\r' {
				return ' '
			}
			return rune(b)
		}
		r, size := utf8.DecodeRune(line[j:])
		ru, ok := s.rules[r]
		if !ok || ru.Group&opt.Groups == 0 || ru.kind == kindKeep {
			return r
		}
		switch ru.kind {
		case kindDelete, kindJoiner, kindBOM:
			j += size
			continue
		case kindEmDash:
			return '-'
		case kindSingleQuote:
			if hasSingle {
				return r
			}
		case kindDoubleQuote:
			if hasDouble {
				return r
			}
		}
		if ru.repl == "" {
			j += size
			continue
		}
		r0, _ := utf8.DecodeRuneInString(ru.repl)
		return r0
	}
	return ' '
}

func isWord(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// firstNonASCII returns the index of the first byte >= 0x80, or -1. It checks
// eight bytes at a time; source files are overwhelmingly ASCII, so this is
// where almost all of the time goes.
func firstNonASCII(b []byte) int {
	i := 0
	for ; i+8 <= len(b); i += 8 {
		if binary.LittleEndian.Uint64(b[i:])&0x8080808080808080 != 0 {
			break
		}
	}
	for ; i < len(b); i++ {
		if b[i] >= utf8.RuneSelf {
			return i
		}
	}
	return -1
}

// IsASCII reports whether b holds only ASCII bytes.
func IsASCII(b []byte) bool { return firstNonASCII(b) < 0 }

// Range is an inclusive range of 1-based line numbers.
type Range struct{ Start, End int }

// LineSet is a set of 1-based line numbers. A nil *LineSet means every line.
type LineSet struct {
	ranges []Range // sorted, merged
}

// NewLineSet builds a set from ranges in any order.
func NewLineSet(ranges []Range) *LineSet {
	rs := make([]Range, 0, len(ranges))
	for _, r := range ranges {
		if r.End >= r.Start {
			rs = append(rs, r)
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].Start < rs[j].Start })
	merged := rs[:0]
	for _, r := range rs {
		if n := len(merged); n > 0 && r.Start <= merged[n-1].End+1 {
			if r.End > merged[n-1].End {
				merged[n-1].End = r.End
			}
			continue
		}
		merged = append(merged, r)
	}
	return &LineSet{ranges: merged}
}

// Has reports whether line n is in the set.
func (ls *LineSet) Has(n int) bool {
	if ls == nil {
		return true
	}
	i := sort.Search(len(ls.ranges), func(i int) bool { return ls.ranges[i].End >= n })
	return i < len(ls.ranges) && ls.ranges[i].Start <= n
}

// Empty reports whether the set selects no line. A nil set is never empty.
func (ls *LineSet) Empty() bool { return ls != nil && len(ls.ranges) == 0 }

// Intersect returns the lines in both sets.
func (ls *LineSet) Intersect(o *LineSet) *LineSet {
	if ls == nil {
		return o
	}
	if o == nil {
		return ls
	}
	var out []Range
	i, j := 0, 0
	for i < len(ls.ranges) && j < len(o.ranges) {
		a, b := ls.ranges[i], o.ranges[j]
		lo, hi := max(a.Start, b.Start), min(a.End, b.End)
		if lo <= hi {
			out = append(out, Range{lo, hi})
		}
		if a.End < b.End {
			i++
		} else {
			j++
		}
	}
	return &LineSet{ranges: out}
}

// Ranges returns the set's ranges (nil for the all-lines set).
func (ls *LineSet) Ranges() []Range {
	if ls == nil {
		return nil
	}
	return ls.ranges
}

var pragma = []byte("deslop:")

// Result is the outcome of scrubbing a whole file.
type Result struct {
	Out      []byte    // the new content; the input itself when nothing changed
	Changed  []int     // 1-based numbers of the lines that changed
	Findings []Finding // every finding, including Manual ones
	Invalid  bool      // not valid UTF-8, so left untouched
}

// File scrubs content, rewriting only the lines in sel (nil: every line).
// Lines are honoured as ignored when they carry a pragma:
//
//	deslop:ignore             this line
//	deslop:ignore-next-line   the following line
//	deslop:off ... deslop:on  everything in between
func (s *Scrubber) File(content []byte, sel *LineSet, opt Options) Result {
	res := Result{Out: content}
	first := firstNonASCII(content)
	if sel.Empty() || first < 0 || opt.Groups == NoGroups {
		return res
	}
	if !utf8.Valid(content[first:]) {
		res.Invalid = true
		return res
	}
	hasPragma := bytes.Contains(content, pragma)

	var out []byte
	off, skipNext := false, false
	lineNo := 0
	for start := 0; start < len(content); {
		lineNo++
		end := bytes.IndexByte(content[start:], '\n')
		next := len(content)
		if end < 0 {
			end = len(content)
		} else {
			end += start
			next = end + 1
		}
		line := content[start:end]

		ignore := false
		if hasPragma {
			ignore = off || skipNext
			skipNext = false
			if bytes.Contains(line, pragma) {
				switch {
				case bytes.Contains(line, []byte("deslop:ignore-next-line")):
					skipNext, ignore = true, true
				case bytes.Contains(line, []byte("deslop:ignore")):
					ignore = true
				case bytes.Contains(line, []byte("deslop:off")):
					off, ignore = true, true
				case bytes.Contains(line, []byte("deslop:on")):
					off, ignore = false, true
				}
			}
		}

		if !ignore && sel.Has(lineNo) {
			newLine, fs := s.Line(line, opt, start == 0, lineNo, res.Findings)
			res.Findings = fs
			if !sameSlice(newLine, line) {
				if out == nil {
					out = make([]byte, start, len(content)+64)
					copy(out, content[:start])
				}
				out = append(out, newLine...)
				out = append(out, content[end:next]...)
				res.Changed = append(res.Changed, lineNo)
				start = next
				continue
			}
		}
		if out != nil {
			out = append(out, content[start:next]...)
		}
		start = next
	}
	if out != nil {
		res.Out = out
	}
	return res
}

// sameSlice reports whether a and b are the same memory, which is how Line
// signals "unchanged" without comparing bytes.
func sameSlice(a, b []byte) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// Rule returns the effective rule for r and whether one exists.
func (s *Scrubber) Rule(r rune) (Rule, bool) {
	ru, ok := s.rules[r]
	return ru, ok && ru.kind != kindKeep
}
