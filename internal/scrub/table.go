package scrub

import (
	"fmt"
	"strings"
)

// Group is a set of character families that can be switched on or off
// together, per repository (.deslop-hook.toml) or per path (.gitattributes).
type Group uint8

const (
	// Invisible covers zero-width characters, bidi controls, soft hyphens,
	// stray byte order marks and the Unicode space variants.
	Invisible Group = 1 << iota
	// Typography covers dashes, curly quotes and the ellipsis.
	Typography
	// Symbols covers arrows, bullets and comparison/multiplication signs.
	Symbols

	// NoGroups disables every rule.
	NoGroups Group = 0
	// AllGroups enables every rule.
	AllGroups = Invisible | Typography | Symbols
)

var groupNames = []struct {
	name  string
	group Group
}{
	{"invisible", Invisible},
	{"typography", Typography},
	{"symbols", Symbols},
}

// ParseGroups parses a comma-separated list of group names. "all" and
// "none" are accepted as shorthands.
func ParseGroups(s string) (Group, error) {
	var g Group
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		switch part {
		case "":
			continue
		case "all":
			g |= AllGroups
			continue
		case "none":
			continue
		}
		found := false
		for _, gn := range groupNames {
			if gn.name == part {
				g |= gn.group
				found = true
				break
			}
		}
		if !found {
			return 0, fmt.Errorf("unknown group %q (want invisible, typography, symbols, all or none)", part)
		}
	}
	return g, nil
}

func (g Group) String() string {
	if g == NoGroups {
		return "none"
	}
	var names []string
	for _, gn := range groupNames {
		if g&gn.group != 0 {
			names = append(names, gn.name)
		}
	}
	return strings.Join(names, ",")
}

type kind uint8

const (
	kindDelete      kind = iota // removed
	kindSpace                   // becomes one ASCII space
	kindReplace                 // becomes Rule.Repl
	kindEmDash                  // " - " between two letters/digits, otherwise "-"
	kindJoiner                  // removed unless both neighbours are non-ASCII (emoji, Persian, Indic)
	kindBOM                     // removed unless it is the first byte of the file
	kindSingleQuote             // "'" unless the line already has one (quote safety)
	kindDoubleQuote             // `"` unless the line already has one (quote safety)
	kindKeep                    // switched off by a config override
)

// Rule says what happens to one character.
type Rule struct {
	Group Group
	Name  string
	kind  kind
	repl  string
}

func rule(g Group, k kind, repl, name string) Rule {
	return Rule{Group: g, Name: name, kind: k, repl: repl}
}

// defaultRules is the built-in character table. Everything not listed here
// is left alone.
var defaultRules = map[rune]Rule{
	// Invisible: deleted.
	0x200B: rule(Invisible, kindDelete, "", "ZERO WIDTH SPACE"),
	0x2060: rule(Invisible, kindDelete, "", "WORD JOINER"),
	0x200E: rule(Invisible, kindDelete, "", "LEFT-TO-RIGHT MARK"),
	0x200F: rule(Invisible, kindDelete, "", "RIGHT-TO-LEFT MARK"),
	0x202A: rule(Invisible, kindDelete, "", "LEFT-TO-RIGHT EMBEDDING"),
	0x202B: rule(Invisible, kindDelete, "", "RIGHT-TO-LEFT EMBEDDING"),
	0x202C: rule(Invisible, kindDelete, "", "POP DIRECTIONAL FORMATTING"),
	0x202D: rule(Invisible, kindDelete, "", "LEFT-TO-RIGHT OVERRIDE"),
	0x202E: rule(Invisible, kindDelete, "", "RIGHT-TO-LEFT OVERRIDE"),
	0x2066: rule(Invisible, kindDelete, "", "LEFT-TO-RIGHT ISOLATE"),
	0x2067: rule(Invisible, kindDelete, "", "RIGHT-TO-LEFT ISOLATE"),
	0x2068: rule(Invisible, kindDelete, "", "FIRST STRONG ISOLATE"),
	0x2069: rule(Invisible, kindDelete, "", "POP DIRECTIONAL ISOLATE"),
	0x00AD: rule(Invisible, kindDelete, "", "SOFT HYPHEN"),
	0xFEFF: rule(Invisible, kindBOM, "", "BYTE ORDER MARK"),
	0x200C: rule(Invisible, kindJoiner, "", "ZERO WIDTH NON-JOINER"),
	0x200D: rule(Invisible, kindJoiner, "", "ZERO WIDTH JOINER"),

	// Invisible: become an ASCII space.
	0x00A0: rule(Invisible, kindSpace, " ", "NO-BREAK SPACE"),
	0x202F: rule(Invisible, kindSpace, " ", "NARROW NO-BREAK SPACE"),
	0x2000: rule(Invisible, kindSpace, " ", "EN QUAD"),
	0x2001: rule(Invisible, kindSpace, " ", "EM QUAD"),
	0x2002: rule(Invisible, kindSpace, " ", "EN SPACE"),
	0x2003: rule(Invisible, kindSpace, " ", "EM SPACE"),
	0x2004: rule(Invisible, kindSpace, " ", "THREE-PER-EM SPACE"),
	0x2005: rule(Invisible, kindSpace, " ", "FOUR-PER-EM SPACE"),
	0x2006: rule(Invisible, kindSpace, " ", "SIX-PER-EM SPACE"),
	0x2007: rule(Invisible, kindSpace, " ", "FIGURE SPACE"),
	0x2008: rule(Invisible, kindSpace, " ", "PUNCTUATION SPACE"),
	0x2009: rule(Invisible, kindSpace, " ", "THIN SPACE"),
	0x200A: rule(Invisible, kindSpace, " ", "HAIR SPACE"),
	0x205F: rule(Invisible, kindSpace, " ", "MEDIUM MATHEMATICAL SPACE"),

	// Typography.
	0x2014: rule(Typography, kindEmDash, "-", "EM DASH"),
	0x2013: rule(Typography, kindReplace, "-", "EN DASH"),
	0x2010: rule(Typography, kindReplace, "-", "HYPHEN"),
	0x2011: rule(Typography, kindReplace, "-", "NON-BREAKING HYPHEN"),
	0x2012: rule(Typography, kindReplace, "-", "FIGURE DASH"),
	0x2015: rule(Typography, kindReplace, "-", "HORIZONTAL BAR"),
	0x2212: rule(Typography, kindReplace, "-", "MINUS SIGN"),
	0x2018: rule(Typography, kindSingleQuote, "'", "LEFT SINGLE QUOTATION MARK"),
	0x2019: rule(Typography, kindSingleQuote, "'", "RIGHT SINGLE QUOTATION MARK"),
	0x201A: rule(Typography, kindSingleQuote, "'", "SINGLE LOW-9 QUOTATION MARK"),
	0x201B: rule(Typography, kindSingleQuote, "'", "SINGLE HIGH-REVERSED-9 QUOTATION MARK"),
	0x201C: rule(Typography, kindDoubleQuote, `"`, "LEFT DOUBLE QUOTATION MARK"),
	0x201D: rule(Typography, kindDoubleQuote, `"`, "RIGHT DOUBLE QUOTATION MARK"),
	0x201E: rule(Typography, kindDoubleQuote, `"`, "DOUBLE LOW-9 QUOTATION MARK"),
	0x201F: rule(Typography, kindDoubleQuote, `"`, "DOUBLE HIGH-REVERSED-9 QUOTATION MARK"),
	0x2026: rule(Typography, kindReplace, "...", "HORIZONTAL ELLIPSIS"),

	// Symbols.
	0x2192: rule(Symbols, kindReplace, "->", "RIGHTWARDS ARROW"),
	0x2190: rule(Symbols, kindReplace, "<-", "LEFTWARDS ARROW"),
	0x21D2: rule(Symbols, kindReplace, "=>", "RIGHTWARDS DOUBLE ARROW"),
	0x2194: rule(Symbols, kindReplace, "<->", "LEFT RIGHT ARROW"),
	0x2022: rule(Symbols, kindReplace, "-", "BULLET"),
	0x2264: rule(Symbols, kindReplace, "<=", "LESS-THAN OR EQUAL TO"),
	0x2265: rule(Symbols, kindReplace, ">=", "GREATER-THAN OR EQUAL TO"),
	0x2260: rule(Symbols, kindReplace, "!=", "NOT EQUAL TO"),
	0x00D7: rule(Symbols, kindReplace, "x", "MULTIPLICATION SIGN"),
}

// Keep is the override value that switches a character off.
const Keep = "keep"
