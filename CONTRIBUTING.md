# Contributing

Thanks for helping. Bug reports with a reproducing repository (or the commands
to build one) are the most useful thing you can send.

## Development

```sh
go test ./...                     # unit + integration tests (needs git)
go test -race ./...
go test -run=NONE -fuzz=FuzzScrub -fuzztime=60s ./internal/scrub
go test -run=NONE -bench=. ./internal/scrub
go run ./cmd/deslop-hook check --all   # the repository must stay clean
```

The integration tests in `cmd/deslop-hook` build the binary and drive real
`git commit`s in temporary repositories: plain, `-a`, `<paths>`, `--amend`,
merges, partial staging, autocrlf, sparse checkouts, linked worktrees,
renames, SHA-256 repositories. A change to how files are read or written needs a
test there.

Layout:

| Package | What it does |
| --- | --- |
| `internal/scrub` | the character table and the per-line transform |
| `internal/gitx` | git plumbing: diffs, attributes, blobs, the index |
| `internal/hook` | the commands: staged lines, a base range, whole files |
| `internal/install` | the repository hook block and the global config hook |
| `internal/config` | `.deslop-hook.toml` and `.gitattributes` handling |

Rules of thumb:

- Keep the source ASCII. Write test characters as escapes (`"\u2014"`), which is
  also what we recommend to users.
- The transform must stay idempotent and give the same result for LF and CRLF
  lines; the fuzz test checks both.
- Never add a dependency without a good reason; there is one today.

## Releasing

Maintainers push a `v*` tag from `main`. `.github/workflows/release.yml` runs
CI, then GoReleaser (GitHub Release, attestations, Homebrew cask), then
`scripts/npm-publish.mjs`, which publishes the npm packages described by
`npm/package.json`: that file sets the package name (and so the names of the six
per-platform packages) and its metadata. It is marked `private` so it cannot be
published by hand; the release script drops the flag and sets the version. Tags with a pre-release suffix (`v0.2.0-rc.1`) skip
the Homebrew tap and go to npm under the `next` tag. Never move or delete a
published tag: the Go checksum database remembers the first one forever.
