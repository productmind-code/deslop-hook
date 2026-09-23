package scrub

import (
	"strings"
	"testing"
)

// A typical source file: all ASCII, so File must take the fast path.
var asciiCorpus = []byte(strings.Repeat("\tif err := run(ctx, args); err != nil { return fmt.Errorf(\"run: %w\", err) }\n", 1<<14))

// A file where every tenth line carries slop.
var mixedCorpus = func() []byte {
	var b strings.Builder
	for i := 0; i < 1<<14; i++ {
		if i%10 == 0 {
			b.WriteString("// the parser\u2014which is fast\u2014handles \u201cquotes\u201d \u2192 fine\u2026\n")
			continue
		}
		b.WriteString("\tif err := run(ctx, args); err != nil { return fmt.Errorf(\"run: %w\", err) }\n")
	}
	return []byte(b.String())
}()

func BenchmarkFileASCII(b *testing.B) {
	s := New(nil)
	b.SetBytes(int64(len(asciiCorpus)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s.File(asciiCorpus, nil, all)
	}
}

func BenchmarkFileMixed(b *testing.B) {
	s := New(nil)
	b.SetBytes(int64(len(mixedCorpus)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s.File(mixedCorpus, nil, all)
	}
}
