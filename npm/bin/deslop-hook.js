#!/usr/bin/env node
// Launcher for the deslop-hook binary. npm/bun install exactly one of the
// <name>-<platform>-<arch> optional dependencies (the one matching this
// machine, e.g. @productmind/deslop-hook-linux-x64); this script finds it and
// runs it. No postinstall step is involved, so it works with `--ignore-scripts`
// and with Bun's default of not running dependency lifecycle scripts.
"use strict";

const { spawnSync } = require("node:child_process");
const path = require("node:path");
const { name } = require("../package.json");

const exe = process.platform === "win32" ? "deslop-hook.exe" : "deslop-hook";
const pkg = `${name}-${process.platform}-${process.arch}`;

let binary;
try {
  binary = require.resolve(`${pkg}/bin/${exe}`);
} catch {
  // A package manager that skipped optional dependencies (--no-optional,
  // --omit=optional) or an unsupported platform.
  process.stderr.write(
    `deslop-hook: the ${pkg} package is not installed.\n` +
      `Reinstall without --no-optional / --omit=optional, or install the binary another way:\n` +
      `  https://github.com/productmind-code/deslop-hook#install\n`,
  );
  process.exit(1);
}

const result = spawnSync(binary, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
  process.stderr.write(`deslop-hook: could not run ${path.relative(process.cwd(), binary)}: ${result.error.message}\n`);
  process.exit(1);
}
if (result.signal) {
  process.kill(process.pid, result.signal);
}
process.exit(result.status ?? 1);
