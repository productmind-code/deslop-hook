# deslop-hook

A git pre-commit hook that removes the Unicode characters language models leave in
code and docs: em dashes, curly quotes, ellipses, non-breaking and zero-width
spaces, bidi controls, arrows. It rewrites only the lines you are committing, in
both the commit and your working tree, and gets out of the way.

```console
$ git commit -m "Add retry logic"
deslop-hook: cleaned 5 characters in 2 files
  src/retry.ts  em dash x2, right single quotation mark x1, rightwards arrow x1
  README.md     no-break space x1
[main 3f9c2e1] Add retry logic
```

It is a single static binary written in Go. It makes a handful of git calls per
commit, adds about 10 ms to a commit when there is nothing to clean, and scans
6,000 files in about 0.2 s.

- [Install](#install)
- [Set up a repository](#set-up-a-repository)
- [What it changes](#what-it-changes)
- [Keeping a character on purpose](#keeping-a-character-on-purpose)
- [Commands](#commands)
- [CI](#ci)
- [Hook managers](#hook-managers)
- [Configuration](#configuration)
- [How it works](#how-it-works)

## Install

| | |
| --- | --- |
| Homebrew (macOS) | `brew install productmind-code/tap/deslop-hook` |
| npm / Bun / pnpm (per project) | `npm install -D @productmind/deslop-hook` / `bun add -d @productmind/deslop-hook` |
| macOS, Linux | `curl -fsSL https://github.com/productmind-code/deslop-hook/releases/latest/download/install.sh \| sh` |
| Windows (PowerShell) | `irm https://github.com/productmind-code/deslop-hook/releases/latest/download/install.ps1 \| iex` |
| Go | `go install github.com/productmind-code/deslop-hook/cmd/deslop-hook@latest` |
| pre-commit framework | see [Hook managers](#hook-managers) |

Every release archive is listed in `checksums.txt` (the install scripts check it)
and carries a build-provenance attestation:
`gh attestation verify deslop-hook_*.tar.gz -R productmind-code/deslop-hook`.

The npm package has no install script: it pulls the right binary in as an
optional dependency (`@productmind/deslop-hook-linux-x64` and so on), so it works
with `--ignore-scripts` and with Bun's default of not running dependency scripts.
The command it installs is still `deslop-hook`.

## Set up a repository

```sh
deslop-hook install
```

This adds a small block to `.git/hooks/pre-commit` (or wherever `core.hooksPath`
points). An existing hook keeps working: a shell hook gets the block after its
first line, a hook in another language is moved aside and run after deslop-hook.
`deslop-hook uninstall` puts everything back.

To set it up for everyone on a JavaScript project, add it as a dev dependency and
install the hook from `prepare`:

```json
{
  "scripts": { "prepare": "deslop-hook install --quiet || exit 0" },
  "devDependencies": { "@productmind/deslop-hook": "^0.1.0" }
}
```

`install` does nothing in CI or outside a git work tree, so `prepare` is safe in
Docker builds and pipelines.

### Every repository on a machine (git 2.54+)

```sh
deslop-hook install --global
```

This uses git's config-based hooks (`hook.deslop.command` in `~/.gitconfig`),
which run alongside each repository's own hooks. Because it reaches repositories
you have not asked it to change, the global hook only reports in a repository
until that repository opts in, by having a `.deslop-hook.toml`, a `deslop`
attribute in `.gitattributes`, or `deslop-hook install`. Turn it off for one
repository with `git config hook.deslop.enabled false`.

## What it changes

Only characters on the lines a commit adds or modifies. Old text is never
touched by the hook; use `deslop-hook fix --all` for a one-off sweep.

| Group | Characters | Become |
| --- | --- | --- |
| invisible | zero-width space, word joiner, left-to-right and right-to-left marks, bidi embeddings, overrides and isolates (the "Trojan Source" characters), soft hyphen, a byte order mark anywhere but the start of the file | removed |
| invisible | zero-width joiner and non-joiner | removed, unless both neighbours are non-ASCII (emoji sequences such as &#x1F9D1;&#x200D;&#x2696;&#xFE0F; and Persian or Indic text keep them) |
| invisible | no-break space, narrow no-break space, en/em/thin/hair and the other Unicode spaces | a space |
| typography | &#x2014; em dash | ` - ` between two letters or digits, `-` otherwise (so a regex class like [&#x2014;:-] does not become a range) |
| typography | &#x2013; &#x2010; &#x2011; &#x2012; &#x2015; &#x2212; (en dash, hyphens, minus) | `-` |
| typography | &#x2018; &#x2019; &#x201A; &#x201B; | `'` |
| typography | &#x201C; &#x201D; &#x201E; &#x201F; | `"` |
| typography | &#x2026; | `...` |
| symbols | &#x2192; &#x2190; &#x21D2; &#x2194; | `->` `<-` `=>` `<->` |
| symbols | &#x2022; | `-` |
| symbols | &#x2264; &#x2265; &#x2260; &#x00D7; | `<=` `>=` `!=` `x` |

Some things are deliberately left alone:

- **A curly quote on a line that already has the matching straight quote**, in
  code files. It is probably inside a string, as in "shown as &#x201C;this&#x201D;", where
  straightening it would end the string early. The hook lists it for you to fix
  by hand and lets the commit through. In prose files (`.md`, `.mdx`, `.txt`,
  `.rst`, `.adoc`) quotes are always straightened.
- Escapes such as `\u2014` or `&mdash;`: only literal characters are changed.
- Binary files, files with a `filter` attribute (Git LFS, git-crypt), a
  `working-tree-encoding`, or `linguist-generated`, files larger than 5 MB,
  symlinks, submodules, and anything that is not valid UTF-8.

## Keeping a character on purpose

Sometimes the character is the point: legal wording, a separator another program
parses, a test fixture. In order of preference:

1. **Write it as an escape**: `"\u2014"` in most languages. The hook never
   touches escapes, and the intent is visible to the next reader.
2. **Mark the line**: a comment containing `deslop:ignore` keeps that line;
   `deslop:ignore-next-line` keeps the next one; `deslop:off` ... `deslop:on`
   keeps a block.
3. **Exclude or narrow paths** in `.gitattributes`:

   ```gitattributes
   src/legal/**          -deslop              # never touched
   **/*.test.ts          deslop=invisible     # only invisible characters
   docs/**               deslop=invisible,typography
   ```

4. **Switch a character off for the repository** in `.deslop-hook.toml` (below).

## Commands

```text
deslop-hook install [--global] [--quiet]   install the pre-commit hook
deslop-hook uninstall [--global]           remove it
deslop-hook check                          lines staged for the next commit; exits 1 on findings
deslop-hook check --base origin/main       lines added since a revision (CI)
deslop-hook check --all | <paths>          whole files
deslop-hook fix --all | <paths>            rewrite whole files in the working tree (never stages)
deslop-hook fix --all --dry-run            list what fix would change
deslop-hook hook                           what the git hook runs
```

`check` accepts `--format github` for pull-request annotations and a step summary.
Add `-v` to any command to see which files were skipped and why.

Skip the hook for one commit with `DESLOP_SKIP=1 git commit ...` or
`git commit --no-verify`.

## CI

The hook runs on developers' machines; a CI check catches commits made without
it. It looks only at the lines a pull request adds.

```yaml
name: deslop
on: pull_request
permissions:
  contents: read
jobs:
  deslop:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with:
          fetch-depth: 2 # the merge commit and the base branch tip
      - run: curl -fsSL https://github.com/productmind-code/deslop-hook/releases/download/v0.1.0/install.sh | DESLOP_HOOK_VERSION=0.1.0 sh
      - run: deslop-hook check --base HEAD^1 --format github
```

On a pull request, `HEAD` is the merge commit and `HEAD^1` the base branch tip, so
`--base HEAD^1` means "what this pull request adds".

## Hook managers

deslop-hook installs itself into a plain `.git/hooks/pre-commit`. If a hook
manager owns that file, `deslop-hook install` tells you what to add instead:

**husky** (`.husky/pre-commit`):

```sh
deslop-hook hook
```

**lefthook** (`lefthook.yml`):

```yaml
pre-commit:
  commands:
    deslop-hook:
      run: deslop-hook hook
```

**pre-commit** (`.pre-commit-config.yaml`):

```yaml
- repo: https://github.com/productmind-code/deslop-hook
  rev: v0.1.0
  hooks:
    - id: deslop-hook        # or deslop-hook-check to only report
```

pre-commit stashes your unstaged changes while hooks run and expects hooks to
change only the working tree, so under pre-commit deslop-hook cleans the working
tree and pre-commit stops the commit with "files were modified by this hook";
`git add` and commit again. If you have unstaged edits right next to a cleaned
line, pre-commit rolls the fix back to protect them and the commit still fails;
stage or stash those edits and retry. With the plain hook, husky or lefthook,
the commit simply goes through with the cleaned lines, and unstaged edits are
left as they are.

## Configuration

Optional. `.deslop-hook.toml` at the top of the repository:

```toml
# Groups to clean: invisible, typography, symbols (default: all three).
groups = ["invisible", "typography"]

# Files where quote safety is off (default below).
prose_extensions = [".md", ".mdx", ".markdown", ".txt", ".rst", ".adoc"]

# Larger files are skipped (bytes; default 5 MB).
max_file_size = 5242880

# Per-character overrides: "keep", or a replacement ("" deletes).
# Keys are the character itself or its code point.
[chars]
"U+2192" = "keep"
"U+2014" = "--"
"U+2713" = "v"      # add a character the defaults do not cover
```

## How it works

On `git commit`, the hook:

1. Asks git which lines the commit adds (`git diff --cached -U0`). In a merge,
   only lines that are new relative to every parent count, so the other branch's
   code is not rewritten.
2. Reads the staged content of those files in one `git cat-file --batch`,
   rewrites characters on the added lines only, writes the new blobs and updates
   the index in one `git update-index` call. For `git commit <paths>` it updates
   both the temporary index git commits from and the repository index.
3. Applies the same change to the working-tree copy, line by line, but only to
   lines that are identical to the staged ones. If a file is partially staged,
   the unstaged changes stay exactly as they were, and stay unstaged. Before
   rewriting a file it saves the previous content to git's object store and logs
   it in `.git/deslop-hook/last-run`, so nothing is ever lost:
   `git cat-file -p <oid> > <path>` restores it.

The transform works per line, never looks across lines, and is idempotent. A
line gives the same result with LF and CRLF endings, which is why it is safe with
`core.autocrlf`.

deslop-hook collects no telemetry and makes no network calls.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues: [SECURITY.md](SECURITY.md).

## License

MIT
