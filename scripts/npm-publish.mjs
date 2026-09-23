#!/usr/bin/env node
// Publishes deslop-hook to npm from a GoReleaser dist/ directory.
//
// npm/package.json is the template for the main package: its name, metadata
// and bin are used as they are. From it this script makes
//
//   <name>-<os>-<cpu>   one per platform (@productmind/deslop-hook-linux-x64,
//                       ...), holding bin/deslop-hook[.exe]
//   <name>              the launcher, with the platform packages as
//                       exact-pinned optionalDependencies
//
// The template is marked "private" so that running `npm publish` by hand in
// npm/ fails instead of shipping a launcher without binaries; the copy
// published here drops that flag and takes its version from the release tag.
//
// Platform packages go first, and the main package is published only once
// every one of them is visible: the registry processes publishes
// asynchronously, sometimes minutes late and out of order, and the main
// package pins them exactly. A version already on the registry is skipped,
// including one an earlier attempt published that is not visible yet, so a
// failed run can simply be re-run.
//
// Usage: node scripts/npm-publish.mjs [--dry-run] [dist]
//   --dry-run  build and `npm pack` every package, publish nothing
//
// GoReleaser's own npm publisher is a Pro feature and relies on a postinstall
// download, which Bun does not run for dependencies; hence this script.

import { execFileSync, spawnSync } from "node:child_process";
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const args = process.argv.slice(2);
const dryRun = args.includes("--dry-run");
const dist = resolve(args.find((a) => !a.startsWith("--")) ?? "dist");
const root = resolve(import.meta.dirname, "..");

const OS = { linux: "linux", darwin: "darwin", windows: "win32" };
const CPU = { amd64: "x64", arm64: "arm64" };

const template = JSON.parse(readFileSync(join(root, "npm", "package.json"), "utf8"));
const { private: _private, version: _version, ...base } = template;
const mainName = base.name;

const metadata = JSON.parse(readFileSync(join(dist, "metadata.json"), "utf8"));
const version = metadata.version;
if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version)) {
  throw new Error(`unexpected version ${JSON.stringify(version)} in metadata.json`);
}
// Pre-releases go to the "next" dist-tag so a plain install never picks one up.
const distTag = version.includes("-") ? "next" : "latest";

const artifacts = JSON.parse(readFileSync(join(dist, "artifacts.json"), "utf8"))
  .filter((a) => a.type === "Binary" && a.extra?.ID === "deslop-hook");
if (artifacts.length !== 6) {
  throw new Error(`expected 6 binaries in artifacts.json, found ${artifacts.length}`);
}

const work = mkdtempSync(join(tmpdir(), "deslop-hook-npm-"));
const readme = readFileSync(join(root, "README.md"), "utf8");
const license = readFileSync(join(root, "LICENSE"), "utf8");

// Runs npm, passing its stderr (notices, errors) through to the log while
// keeping a copy for callers that need to inspect a failure.
function npm(cwd, ...argv) {
  const r = spawnSync("npm", argv, { cwd, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
  process.stderr.write(r.stderr ?? "");
  if (r.error) throw r.error;
  if (r.status !== 0) {
    throw Object.assign(new Error(`npm ${argv.join(" ")} failed with exit code ${r.status}`), { stderr: r.stderr ?? "" });
  }
  return r.stdout;
}

function published(name) {
  try {
    execFileSync("npm", ["view", `${name}@${version}`, "version", "--prefer-online"], { stdio: ["ignore", "pipe", "ignore"] });
    return true;
  } catch {
    return false;
  }
}

// The registry processes publishes asynchronously and not always in order: a
// version can take minutes to show up. Waiting for every platform package
// before publishing the main one means nobody installs a main package whose
// pinned binary package cannot be fetched yet.
function waitUntilVisible(name) {
  if (dryRun) return;
  const deadline = Date.now() + 30 * 60 * 1000;
  while (!published(name)) {
    if (Date.now() > deadline) {
      throw new Error(`${name}@${version} is still not visible on the registry after 30 minutes`);
    }
    console.log(`waiting for ${name}@${version} to become visible...`);
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 20_000);
  }
}

function publish(dir, name) {
  if (dryRun) {
    const out = npm(dir, "pack", "--json", "--pack-destination", work);
    const [info] = JSON.parse(out);
    console.log(`would publish ${name}@${version} (${info.files.length} files, ${info.size} bytes)`);
    return;
  }
  if (published(name)) {
    console.log(`${name}@${version} is already published, skipping`);
    return;
  }
  // Scoped packages are private by default on npm; these are public.
  try {
    npm(dir, "publish", "--access", "public", "--provenance", "--tag", distTag);
  } catch (e) {
    // Published by an earlier attempt but not visible yet (see above).
    if (/cannot publish over the previously published version/i.test(e.stderr ?? "")) {
      console.log(`${name}@${version} was already published, skipping`);
      return;
    }
    throw e;
  }
  console.log(`published ${name}@${version}`);
}

function write(dir, file, content) {
  writeFileSync(join(dir, file), content);
}

const optionalDependencies = {};
for (const a of artifacts) {
  const os = OS[a.goos];
  const cpu = CPU[a.goarch];
  if (!os || !cpu) throw new Error(`unexpected platform ${a.goos}/${a.goarch}`);
  // Must match npm/bin/deslop-hook.js, which looks for `${name}-${platform}-${arch}`.
  const name = `${mainName}-${os}-${cpu}`;
  const exe = os === "win32" ? "deslop-hook.exe" : "deslop-hook";
  const dir = join(work, `${os}-${cpu}`);
  mkdirSync(join(dir, "bin"), { recursive: true });
  copyFileSync(resolve(root, a.path), join(dir, "bin", exe));
  chmodSync(join(dir, "bin", exe), 0o755);
  write(dir, "LICENSE", license);
  write(dir, "README.md", `# ${name}\n\nThe ${os}-${cpu} binary of [deslop-hook](${base.homepage}). Install \`${mainName}\` instead; it picks this package up automatically.\n`);
  write(dir, "package.json", JSON.stringify({
    name,
    version,
    description: `The ${os}-${cpu} binary for ${mainName}`,
    homepage: base.homepage,
    repository: base.repository,
    license: base.license,
    author: base.author,
    os: [os],
    cpu: [cpu],
    files: ["bin/", "README.md", "LICENSE"],
    preferUnplugged: true,
  }, null, 2) + "\n");
  publish(dir, name);
  optionalDependencies[name] = version;
}

for (const name of Object.keys(optionalDependencies)) {
  waitUntilVisible(name);
}

const main = join(work, "main");
mkdirSync(join(main, "bin"), { recursive: true });
copyFileSync(join(root, "npm", "bin", "deslop-hook.js"), join(main, "bin", "deslop-hook.js"));
chmodSync(join(main, "bin", "deslop-hook.js"), 0o755);
write(main, "README.md", readme);
write(main, "LICENSE", license);
write(main, "package.json", JSON.stringify({ ...base, version, optionalDependencies }, null, 2) + "\n");
publish(main, mainName);

console.log(dryRun ? `dry run complete; packages in ${work}` : `done: ${mainName}@${version} (${distTag})`);
