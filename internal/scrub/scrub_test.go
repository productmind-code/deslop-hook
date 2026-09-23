package scrub

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

var all = Options{Groups: AllGroups}

func line(t *testing.T, s *Scrubber, in string, opt Options) (string, []Finding) {
	t.Helper()
	out, fs := s.Line([]byte(in), opt, false, 1, nil)
	return string(out), fs
}

func TestLine(t *testing.T) {
	s := New(nil)
	prose := Options{Groups: AllGroups, Prose: true}
	cases := []struct {
		name string
		in   string
		opt  Options
		want string
	}{
		{"ascii untouched", "plain ascii - nothing to do", all, "plain ascii - nothing to do"},
		{"unknown non-ascii untouched", "caf\u00e9, na\u00efve, \u65e5\u672c\u8a9e", all, "caf\u00e9, na\u00efve, \u65e5\u672c\u8a9e"},

		{"em dash between words gets spaces", "foo\u2014bar", all, "foo - bar"},
		{"em dash already spaced", "foo \u2014 bar", all, "foo - bar"},
		{"em dash alone", "\u2014", all, "-"},
		{"em dash in regex class", "[\u2014:-]", all, "[-:-]"},
		{"em dash placeholder string", "const EMPTY = \"\u2014\";", all, `const EMPTY = "-";`},
		{"em dash before punctuation", "wait\u2014.", all, "wait-."},
		{"em dash digits", "1\u20142", all, "1 - 2"},
		{"en dash range", "1\u201310", all, "1-10"},
		{"minus sign", "\u22125", all, "-5"},
		{"non-breaking hyphen", "e\u2011mail", all, "e-mail"},
		{"ellipsis", "wait\u2026", all, "wait..."},

		{"curly doubles in comment", "// the \u201cbest\u201d option", all, `// the "best" option`},
		{"curly singles in comment", "// don\u2019t", all, "// don't"},
		{"quote safety double", "help: \"shown as \u201cAgentic\u201d here\",", all, "help: \"shown as \u201cAgentic\u201d here\","},
		{"quote safety single", "x = 'Don\u2019t'", all, "x = 'Don\u2019t'"},
		{"quote safety other kind ok", "x = 'say \u201chi\u201d'", all, `x = 'say "hi"'`},
		{"prose ignores quote safety", "He said \"a\" and \u201cb\u201d", prose, `He said "a" and "b"`},

		{"arrow", "a \u2192 b", all, "a -> b"},
		{"double arrow", "a \u21d2 b", all, "a => b"},
		{"left right arrow", "a \u2194 b", all, "a <-> b"},
		{"bullet", "\u2022 item", all, "- item"},
		{"comparisons", "a \u2264 b \u2265 c \u2260 d", all, "a <= b >= c != d"},
		{"times", "3\u00d74", all, "3x4"},

		{"zwsp deleted", "a\u200bb", all, "ab"},
		{"word joiner deleted", "a\u2060b", all, "ab"},
		{"bidi override deleted", "a\u202eb\u202c", all, "ab"},
		{"isolates deleted", "\u2066x\u2069", all, "x"},
		{"soft hyphen deleted", "hy\u00adphen", all, "hyphen"},
		{"mid-line bom deleted", "a\ufeffb", all, "ab"},
		{"nbsp to space", "a\u00a0b", all, "a b"},
		{"narrow nbsp to space", "10\u202f%", all, "10 %"},
		{"thin space to space", "a\u2009b", all, "a b"},
		{"zwj in emoji kept", "\U0001f9d1\u200d\u2696\ufe0f", all, "\U0001f9d1\u200d\u2696\ufe0f"},
		{"zwj next to ascii deleted", "a\u200db", all, "ab"},
		{"zwnj in persian kept", "\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645", all, "\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645"},

		{"nbsp then em dash", "a\u00a0\u2014b", all, "a -b"},
		{"zwsp does not hide a word neighbour", "foo\u2014\u200bbar", all, "foo - bar"},
		{"trailing CR is whitespace", "foo\u2014\r", all, "foo-\r"},

		{"group off", "a\u2014b\u200b", Options{Groups: Invisible}, "a\u2014b"},
		{"no groups", "a\u2014b", Options{Groups: NoGroups}, "a\u2014b"},
		{"invalid utf8 left alone", "a\xffb\u2014c", all, "a\xffb\u2014c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := line(t, s, tc.in, tc.opt)
			if got != tc.want {
				t.Fatalf("Line(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLineUnchangedReturnsSameSlice(t *testing.T) {
	s := New(nil)
	in := []byte("caf\u00e9 \U0001f9d1\u200d\u2696")
	out, fs := s.Line(in, all, false, 1, nil)
	if !sameSlice(out, in) || len(fs) != 0 {
		t.Fatalf("expected the input slice back and no findings, got %q %v", out, fs)
	}
}

func TestBOMAtFileStartKept(t *testing.T) {
	s := New(nil)
	res := s.File([]byte("\ufeffhello\n\ufeffworld\n"), nil, all)
	if got := string(res.Out); got != "\ufeffhello\nworld\n" {
		t.Fatalf("got %q", got)
	}
	if len(res.Findings) != 1 || res.Findings[0].Line != 2 {
		t.Fatalf("findings: %+v", res.Findings)
	}
}

func TestFindings(t *testing.T) {
	s := New(nil)
	_, fs := s.Line([]byte("ab\u2014c \u201cq\u201d \"x\""), all, false, 7, nil)
	if len(fs) != 3 {
		t.Fatalf("want 3 findings, got %+v", fs)
	}
	if f := fs[0]; f.Line != 7 || f.Col != 3 || f.Rune != 0x2014 || f.Repl != " - " || f.Manual {
		t.Fatalf("em dash finding: %+v", f)
	}
	if f := fs[1]; f.Col != 6 || !f.Manual || f.Name != "LEFT DOUBLE QUOTATION MARK" {
		t.Fatalf("quote finding: %+v", f)
	}
}

func TestOverrides(t *testing.T) {
	s := New(map[rune]string{0x2192: Keep, 0x2014: "--", 0x2713: "v"})
	got, _ := line(t, s, "a \u2192 b\u2014c \u2713", all)
	if want := "a \u2192 b--c v"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if _, ok := s.Rule(0x2192); ok {
		t.Fatal("kept rune should report no active rule")
	}
}

func TestFileLineSelection(t *testing.T) {
	s := New(nil)
	in := "one\u2014a\ntwo\u2014b\nthree\u2014c\n"
	res := s.File([]byte(in), NewLineSet([]Range{{2, 2}}), all)
	if want := "one\u2014a\ntwo - b\nthree\u2014c\n"; string(res.Out) != want {
		t.Fatalf("got %q want %q", res.Out, want)
	}
	if len(res.Changed) != 1 || res.Changed[0] != 2 {
		t.Fatalf("changed = %v", res.Changed)
	}
}

func TestFilePragmas(t *testing.T) {
	s := New(nil)
	in := strings.Join([]string{
		"a\u2014b",
		"keep\u2014this // deslop:ignore",
		"// deslop:ignore-next-line",
		"keep\u2014too",
		"fix\u2014me",
		"/* deslop:off */",
		"x\u2014y",
		"/* deslop:on */",
		"z\u2014w",
		"no newline at end\u2026",
	}, "\n")
	want := strings.Join([]string{
		"a - b",
		"keep\u2014this // deslop:ignore",
		"// deslop:ignore-next-line",
		"keep\u2014too",
		"fix - me",
		"/* deslop:off */",
		"x\u2014y",
		"/* deslop:on */",
		"z - w",
		"no newline at end...",
	}, "\n")
	res := s.File([]byte(in), nil, all)
	if string(res.Out) != want {
		t.Fatalf("got\n%s\nwant\n%s", res.Out, want)
	}
}

func TestFileASCIIReturnsInput(t *testing.T) {
	s := New(nil)
	in := []byte(strings.Repeat("hello world\n", 100))
	res := s.File(in, nil, all)
	if !sameSlice(res.Out, in) || res.Changed != nil {
		t.Fatal("ASCII input must come back untouched")
	}
}

func TestCRLFMatchesLF(t *testing.T) {
	s := New(nil)
	lf := "a\u2014b\n\u201cq\u201d\u2014\nx\u00a0\u2014\n"
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	a := s.File([]byte(lf), nil, all)
	b := s.File([]byte(crlf), nil, all)
	if got := strings.ReplaceAll(string(b.Out), "\r\n", "\n"); got != string(a.Out) {
		t.Fatalf("CRLF %q != LF %q", got, a.Out)
	}
}

func TestLineSet(t *testing.T) {
	ls := NewLineSet([]Range{{5, 7}, {1, 2}, {3, 3}, {10, 9}})
	for n, want := range map[int]bool{1: true, 3: true, 4: false, 5: true, 7: true, 8: false, 9: false, 10: false} {
		if ls.Has(n) != want {
			t.Errorf("Has(%d) = %v", n, !want)
		}
	}
	if got := ls.Ranges(); len(got) != 2 || got[0] != (Range{1, 3}) {
		t.Fatalf("merge: %v", got)
	}
	x := ls.Intersect(NewLineSet([]Range{{2, 6}}))
	if got := x.Ranges(); len(got) != 2 || got[0] != (Range{2, 3}) || got[1] != (Range{5, 6}) {
		t.Fatalf("intersect: %v", got)
	}
	var nilSet *LineSet
	if !nilSet.Has(42) || nilSet.Empty() {
		t.Fatal("nil set selects everything")
	}
	if !NewLineSet(nil).Empty() {
		t.Fatal("empty set")
	}
}

func TestParseGroups(t *testing.T) {
	g, err := ParseGroups("invisible, typography")
	if err != nil || g != Invisible|Typography {
		t.Fatalf("got %v %v", g, err)
	}
	if g, _ := ParseGroups("all"); g != AllGroups {
		t.Fatal("all")
	}
	if g, _ := ParseGroups("none"); g != NoGroups {
		t.Fatal("none")
	}
	if _, err := ParseGroups("emoji"); err == nil {
		t.Fatal("want error")
	}
}

func FuzzScrub(f *testing.F) {
	for _, seed := range []string{
		"0000000\xe2\x80\u200b\x8b0", "a\u2014b", "\u201cq\u201d 'x'", "\U0001f9d1\u200d\u2696", "a\u200b\u200d\u00e9",
		"a\u00a0\u2014b\r", "\ufeff\ufeff", "x\xff\u2026", "[\u2014:-]", "\u200d\u200d\u2014\u200c",
	} {
		f.Add(seed, true)
		f.Add(seed, false)
	}
	s := New(nil)
	f.Fuzz(func(t *testing.T, in string, prose bool) {
		opt := Options{Groups: AllGroups, Prose: prose}
		once := s.File([]byte(in), nil, opt)
		twice := s.File(once.Out, nil, opt)
		if !bytes.Equal(once.Out, twice.Out) {
			t.Fatalf("not idempotent: %q -> %q -> %q", in, once.Out, twice.Out)
		}
		if utf8.ValidString(in) && !utf8.Valid(once.Out) {
			t.Fatalf("valid input produced invalid output: %q -> %q", in, once.Out)
		}
		if IsASCII([]byte(in)) && string(once.Out) != in {
			t.Fatalf("ASCII input changed: %q -> %q", in, once.Out)
		}
		// Invalid UTF-8 is never touched.
		if !utf8.ValidString(in) && (string(once.Out) != in || !once.Invalid) {
			t.Fatalf("invalid input changed: %q -> %q", in, once.Out)
		}
		// CRLF and LF give the same result.
		if !strings.Contains(in, "\r") {
			crlf := strings.ReplaceAll(in, "\n", "\r\n")
			got := strings.ReplaceAll(string(s.File([]byte(crlf), nil, opt).Out), "\r\n", "\n")
			if got != string(once.Out) {
				t.Fatalf("CRLF differs: %q vs %q", got, once.Out)
			}
		}
	})
}
