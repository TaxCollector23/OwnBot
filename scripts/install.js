#!/usr/bin/env node
// Install the prebuilt, checksummed binaries for this npm package version.
// Release artifacts are produced by scripts/release/pipeline.py.
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const OWNER = 'TaxCollector23';
const REPOSITORY = 'OwnBot';
const TARGETS = new Set([
  'darwin-arm64', 'darwin-amd64', 'linux-arm64', 'linux-amd64',
  'windows-amd64', 'freebsd-amd64',
]);

function targetName(platform = process.platform, arch = process.arch) {
  const targetArch = arch === 'x64' ? 'amd64' : arch;
  const targetOS = platform === 'win32' ? 'windows' : platform;
  const normalized = `${targetOS}-${targetArch}`;
  if (!TARGETS.has(normalized)) {
    throw new Error(`OwnBot has no prebuilt release for ${platform}/${arch}. Supported targets: macOS arm64/x64, Linux arm64/x64, Windows x64, and FreeBSD x64. See https://github.com/${OWNER}/${REPOSITORY}/releases for supported builds.`);
  }
  return normalized;
}

function parseChecksums(text) {
  const results = new Map();
  for (const line of text.split(/\r?\n/)) {
    if (!line.trim()) continue;
    const match = /^([a-f0-9]{64})  ([A-Za-z0-9._-]+)$/.exec(line);
    if (!match || results.has(match[2])) throw new Error('release checksum file is malformed');
    results.set(match[2], match[1]);
  }
  return results;
}

async function download(url, maxBytes = 1024 * 1024) {
  const response = await fetch(url, { redirect: 'follow', signal: AbortSignal.timeout(120000) });
  if (!response.ok) throw new Error(`download returned HTTP ${response.status}`);
  const declaredLength = Number(response.headers.get('content-length'));
  if (Number.isFinite(declaredLength) && declaredLength > maxBytes) throw new Error('release asset exceeded its allowed download size');
  if (!response.body) throw new Error('download returned an empty response');
  const reader = response.body.getReader();
  const chunks = [];
  let received = 0;
  try {
    while (true) {
      const {done, value} = await reader.read();
      if (done) break;
      received += value.byteLength;
      if (received > maxBytes) {
        await reader.cancel();
        throw new Error('release asset exceeded its allowed download size');
      }
      chunks.push(Buffer.from(value));
    }
  } finally {
    reader.releaseLock();
  }
  return Buffer.concat(chunks, received);
}

function digest(bytes) {
  return crypto.createHash('sha256').update(bytes).digest('hex');
}

function extract(archivePath, destination, windows) {
  fs.mkdirSync(destination, { recursive: true });
  let result;
  if (windows) {
    const command = "Add-Type -AssemblyName System.IO.Compression.FileSystem; [System.IO.Compression.ZipFile]::ExtractToDirectory($env:OWNBOT_ARCHIVE, $env:OWNBOT_EXTRACT_DIR)";
    result = spawnSync('powershell.exe', ['-NoLogo', '-NoProfile', '-NonInteractive', '-Command', command], {
      env: { ...process.env, OWNBOT_ARCHIVE: archivePath, OWNBOT_EXTRACT_DIR: destination },
      stdio: 'inherit',
    });
  } else {
    result = spawnSync('tar', ['-xzf', archivePath, '-C', destination, '--', 'ownbot', 'ownbotd'], { stdio: 'inherit' });
  }
  if (result.error || result.status !== 0) {
    const reason = result.error ? result.error.message : `extractor exited with status ${result.status}`;
    throw new Error(`could not unpack the OwnBot release archive (${reason})`);
  }
}

async function install() {
  const metadata = JSON.parse(fs.readFileSync(path.join(ROOT, 'package.json'), 'utf8'));
  const version = metadata.version;
  const target = targetName();
  const [platform, arch] = target.split('-');
  const isWindows = platform === 'windows';
  const suffix = isWindows ? '.zip' : '.tar.gz';
  const archiveName = `ownbot-${version}-${platform}-${arch}${suffix}`;
  const tag = `v${version}`;
  const releaseRoot = (process.env.OWNBOT_RELEASE_BASE_URL || `https://github.com/${OWNER}/${REPOSITORY}/releases/download/${tag}`).replace(/\/$/, '');

  const [manifestBytes, checksumBytes] = await Promise.all([
    download(`${releaseRoot}/manifest.json`, 1024 * 1024),
    download(`${releaseRoot}/SHA256SUMS`, 128 * 1024),
  ]);
  const sums = parseChecksums(checksumBytes.toString('utf8'));
  if (!sums.has('manifest.json') || digest(manifestBytes) !== sums.get('manifest.json')) {
    throw new Error('release manifest failed its SHA-256 integrity check');
  }
  const manifest = JSON.parse(manifestBytes.toString('utf8'));
  if (manifest.name !== 'ownbot' || manifest.version !== version || !Array.isArray(manifest.assets)) {
    throw new Error('release manifest does not match this OwnBot package version');
  }
  const artifact = manifest.assets.find((asset) => asset.os === platform && asset.arch === arch);
  if (!artifact || artifact.file !== archiveName || !/^[a-f0-9]{64}$/.test(artifact.sha256 || '')) {
    throw new Error(`the release does not contain a verified ${platform}/${arch} artifact`);
  }
  if (sums.get(archiveName) !== artifact.sha256) throw new Error('release manifest and checksum list disagree');
  const archiveBytes = await download(`${releaseRoot}/${archiveName}`, 300 * 1024 * 1024);
  if (digest(archiveBytes) !== artifact.sha256) throw new Error(`release archive ${archiveName} failed its SHA-256 integrity check`);

    const stage = fs.mkdtempSync(path.join(os.tmpdir(), 'ownbot-install-'));
  const archivePath = path.join(stage, archiveName);
  const unpacked = path.join(stage, 'unpacked');
  const binaryDirectory = path.join(ROOT, 'bin');
  try {
    fs.writeFileSync(archivePath, archiveBytes, { mode: 0o600 });
    extract(archivePath, unpacked, isWindows);
    const binarySuffix = isWindows ? '.exe' : '';
    for (const name of ['ownbot', 'ownbotd']) {
      const source = path.join(unpacked, `${name}${binarySuffix}`);
      const stat = fs.lstatSync(source);
      if (!stat.isFile() || stat.size < 1024) throw new Error(`release archive did not contain a valid ${name} binary`);
    }
    const installStage = fs.mkdtempSync(path.join(ROOT, '.ownbot-install-'));
    const stagedBin = path.join(installStage, 'bin');
    const backupBin = path.join(installStage, 'previous-bin');
    const binaryDirectory = path.join(ROOT, 'bin');
    fs.mkdirSync(stagedBin);
    let movedExisting = false;
    let installed = false;
    try {
      for (const name of ['ownbot', 'ownbotd']) {
        const source = path.join(unpacked, `${name}${binarySuffix}`);
        const destination = path.join(stagedBin, `${name}${binarySuffix}`);
        fs.copyFileSync(source, destination);
        if (!isWindows) fs.chmodSync(destination, 0o755);
      }
      if (fs.existsSync(binaryDirectory)) {
        fs.renameSync(binaryDirectory, backupBin);
        movedExisting = true;
      }
      try {
        fs.renameSync(stagedBin, binaryDirectory);
        installed = true;
      } catch (error) {
        if (movedExisting && !fs.existsSync(binaryDirectory)) fs.renameSync(backupBin, binaryDirectory);
        throw error;
      }
    } finally {
      if (!installed && movedExisting && fs.existsSync(backupBin) && !fs.existsSync(binaryDirectory)) {
        fs.renameSync(backupBin, binaryDirectory);
      }
      fs.rmSync(installStage, { recursive: true, force: true });
    }
  } finally {
    fs.rmSync(stage, { recursive: true, force: true });
  }
}

if (require.main === module) {
  install().catch((error) => {
    const message = error && error.message ? error.message : String(error);
    console.error(`OwnBot binary installation failed: ${message}`);
    console.error('Check your network connection and retry. npm packages depend on the matching GitHub release assets; if this version has not finished releasing, retry after its release assets are available.');
    process.exitCode = 1;
  });
}

module.exports = { install, parseChecksums, targetName };
