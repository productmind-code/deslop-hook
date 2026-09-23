// Package config loads .deslop-hook.toml and turns git attributes into
// per-file scrub options.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"github.com/productmind-code/deslop-hook/internal/scrub"
)

// FileName is the optional repository config file, read from the top of the
// working tree.
const FileName = ".deslop-hook.toml"

// Attribute is the git attribute that excludes paths (-deslop) or narrows
// the groups for them (deslop=invisible).
const Attribute = "deslop"

// DefaultMaxFileSize is the largest file scrubbed by default.
const DefaultMaxFileSize = 5 << 20

// DefaultProse lists the extensions treated as prose, where quote safety is
// off because curly quotes there are never string delimiters.
var DefaultProse = []string{".md", ".mdx", ".markdown", ".txt", ".rst", ".adoc"}

// Config is the effective configuration for one repository.
type Config struct {
	Groups      scrub.Group
	Prose       map[string]bool
	MaxFileSize int64
	Chars       map[rune]string
	// Present reports whether .deslop-hook.toml exists, which counts as the
	// repository opting in.
	Present bool
}

type fileFormat struct {
	Groups          []string          `toml:"groups"`
	ProseExtensions []string          `toml:"prose_extensions"`
	MaxFileSize     int64             `toml:"max_file_size"`
	Chars           map[string]string `toml:"chars"`
}

// Default returns the configuration used when there is no config file.
func Default() *Config {
	c := &Config{Groups: scrub.AllGroups, Prose: map[string]bool{}, MaxFileSize: DefaultMaxFileSize}
	for _, e := range DefaultProse {
		c.Prose[e] = true
	}
	return c
}

// Load reads dir/.deslop-hook.toml. A missing file gives Default().
func Load(dir string) (*Config, error) {
	c := Default()
	data, err := os.ReadFile(path.Join(dir, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	c.Present = true
	var f fileFormat
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("%s: unknown key %q", FileName, und[0].String())
	}
	if md.IsDefined("groups") {
		g, err := scrub.ParseGroups(strings.Join(f.Groups, ","))
		if err != nil {
			return nil, fmt.Errorf("%s: groups: %w", FileName, err)
		}
		c.Groups = g
	}
	if md.IsDefined("prose_extensions") {
		c.Prose = map[string]bool{}
		for _, e := range f.ProseExtensions {
			e = strings.ToLower(e)
			if !strings.HasPrefix(e, ".") {
				e = "." + e
			}
			c.Prose[e] = true
		}
	}
	if f.MaxFileSize > 0 {
		c.MaxFileSize = f.MaxFileSize
	}
	if len(f.Chars) > 0 {
		c.Chars = map[rune]string{}
		for k, v := range f.Chars {
			r, err := parseChar(k)
			if err != nil {
				return nil, fmt.Errorf("%s: chars: %w", FileName, err)
			}
			c.Chars[r] = v
		}
	}
	return c, nil
}

// parseChar accepts the character itself or its code point as "U+2014".
func parseChar(k string) (rune, error) {
	if up := strings.ToUpper(k); strings.HasPrefix(up, "U+") {
		n, err := strconv.ParseUint(up[2:], 16, 32)
		if err != nil || !utf8.ValidRune(rune(n)) {
			return 0, fmt.Errorf("bad code point %q", k)
		}
		return rune(n), nil
	}
	r, size := utf8.DecodeRuneInString(k)
	if r == utf8.RuneError || size != len(k) {
		return 0, fmt.Errorf("key %q must be one character or U+XXXX", k)
	}
	return r, nil
}

// Attributes queried for every file.
var Attributes = []string{Attribute, "filter", "binary", "text", "working-tree-encoding", "linguist-generated"}

// ForPath returns the scrub options for p given its attributes, or a reason
// to skip it.
func (c *Config) ForPath(p string, attrs map[string]string) (opt scrub.Options, skip string) {
	isSet := func(a string) bool {
		v := attrs[a]
		return v != "" && v != "unspecified" && v != "unset" && v != "false"
	}
	switch v := attrs[Attribute]; v {
	case "unset", "false":
		return opt, "excluded by .gitattributes"
	case "", "unspecified", "set", "true":
		opt.Groups = c.Groups
	default:
		g, err := scrub.ParseGroups(v)
		if err != nil {
			return opt, fmt.Sprintf("bad %s attribute: %v", Attribute, err)
		}
		opt.Groups = g & c.Groups
	}
	switch {
	case isSet("filter"):
		return opt, "has a filter attribute (LFS, git-crypt, ...)"
	case isSet("binary") || attrs["text"] == "unset":
		return opt, "marked binary"
	case isSet("working-tree-encoding"):
		return opt, "has a working-tree-encoding"
	case isSet("linguist-generated"):
		return opt, "marked linguist-generated"
	case opt.Groups == scrub.NoGroups:
		return opt, "no groups enabled"
	}
	opt.Prose = c.Prose[strings.ToLower(path.Ext(p))]
	return opt, ""
}

// Binary reports whether data looks binary: git's own test, a NUL byte in
// the first 8000 bytes.
func Binary(data []byte) bool {
	n := len(data)
	if n > 8000 {
		n = 8000
	}
	for _, b := range data[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}
