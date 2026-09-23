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
// Platform packages go first so the main package never points at a version
// that does not exist yet. A package whose version is already on the registry
// is skipped, so a failed run can simply be re-run.
//
// Usage: node scripts/npm-publish.mjs [--dry-run] [dist]
//   --dry-run  build and `npm pack` every package, publish nothing
//
// GoReleaser's own npm publisher is a Pro feature and relies on a postinstall
// download, which Bun does not run for dependencies; hence this script.

import { execFileSync } from "node:child_process";
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

function npm(cwd, ...argv) {
  return execFileSync("npm", argv, { cwd, stdio: ["ignore", "pipe", "inherit"], encoding: "utf8" });
}

function published(name) {
  try {
    execFileSync("npm", ["view", `${name}@${version}`, "version"], { stdio: ["ignore", "pipe", "ignore"] });
    return true;
  } catch {
    return false;
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
  npm(dir, "publish", "--access", "public", "--provenance", "--tag", distTag);
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

const main = join(work, "main");
mkdirSync(join(main, "bin"), { recursive: true });
copyFileSync(join(root, "npm", "bin", "deslop-hook.js"), join(main, "bin", "deslop-hook.js"));
chmodSync(join(main, "bin", "deslop-hook.js"), 0o755);
write(main, "README.md", readme);
write(main, "LICENSE", license);
write(main, "package.json", JSON.stringify({ ...base, version, optionalDependencies }, null, 2) + "\n");
publish(main, mainName);

console.log(dryRun ? `dry run complete; packages in ${work}` : `done: ${mainName}@${version} (${distTag})`);
