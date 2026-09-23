# Security

deslop-hook runs on every commit on developers' machines and rewrites files, so
we treat security reports as a priority.

## Reporting a vulnerability

Please report privately through GitHub: **Security > Report a vulnerability** on
this repository. Do not open a public issue. We aim to acknowledge reports within
three working days.

Useful things to include: the version (`deslop-hook version`), your OS and git
version, and a repository or steps that reproduce the problem.

## Supported versions

Only the latest release receives fixes. The project is pre-1.0.

## What deslop-hook does and does not do

- It makes no network calls and collects no telemetry.
- It reads and writes only files in the repository it runs in, through git and
  the working tree, and keeps a copy of every working-tree file it rewrites in
  git's object store (listed in `.git/deslop-hook/last-run`).
- Releases are built by GitHub Actions from a tagged commit. Each archive is
  listed in `checksums.txt` and has a build-provenance attestation:
  `gh attestation verify <archive> -R productmind-code/deslop-hook`.
