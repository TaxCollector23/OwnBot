#!/usr/bin/env node
// Download and install only the package-version-matched, signed release.
// Uses Node built-ins only; all archive paths and sizes are checked before write.
'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const https = require('node:https');
const os = require('node:os');
const path = require('node:path');
const zlib = require('node:zlib');

const REPOSITORY = 'TaxCollector23/OwnBot';
const MAX_JSON_BYTES = 1024 * 1024;
const MAX_SIGNATURE_BYTES = 16 * 1024;
const MAX_ARCHIVE_BYTES = 256 * 1024 * 1024;
const MAX_BINARY_BYTES = 128 * 1024 * 1024;
const MAX_README_BYTES = 1024 * 1024;
const TARGETS = new Set(['darwin/arm64', 'darwin/amd64', 'linux/arm64', 'linux/amd64', 'windows/amd64', 'freebsd/amd64']);
const HOSTS = new Set(['api.github.com', 'github.com', 'release-assets.githubusercontent.com', 'objects.githubusercontent.com']);
const DER_ED25519_PREFIX = Buffer.from('302a300506032b6570032100', 'hex');

function targetFor(platform = process.platform, arch = process.arch) {
  const goos = ({ darwin: 'darwin', linux: 'linux', win32: 'windows', freebsd: 'freebsd' })[platform];
  const goarch = ({ arm64: 'arm64', x64: 'amd64' })[arch];
  const target = `${goos}/${goarch}`;
  if (!TARGETS.has(target)) throw new Error(`signed OwnBot packages are not available for ${platform}/${arch}`);
  return { os: goos, arch: goarch };
}

function archiveName(version, target) {
  return `ownbot-${version}-${target.os}-${target.arch}.tar.gz`;
}

function exactObject(value, keys, label) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(`${label} must be an object`);
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  if (actual.length !== expected.length || actual.some((key, index) => key !== expected[index])) {
    throw new Error(`${label} has unexpected or missing fields`);
  }
}

function verifyManifest(rawManifest, rawSignature, keys, target, wantedVersion) {
  let signatureDoc;
  let manifest;
  try {
    signatureDoc = JSON.parse(rawSignature.toString('utf8'));
    manifest = JSON.parse(rawManifest.toString('utf8'));
  } catch {
    throw new Error('release manifest or signature is not valid JSON');
  }
  exactObject(signatureDoc, ['key_id', 'signature'], 'release signature');
  exactObject(manifest, ['name', 'version', 'assets'], 'release manifest');
  if (typeof signatureDoc.key_id !== 'string' || typeof signatureDoc.signature !== 'string') throw new Error('release signature fields are invalid');
  if (typeof manifest.version !== 'string' || !/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/.test(manifest.version) || manifest.name !== 'ownbot') {
    throw new Error('signed release identity or version is invalid');
  }
  if (manifest.version !== wantedVersion.replace(/^v/, '')) throw new Error('signed release version does not match the npm package version');
  const encoded = keys[signatureDoc.key_id];
  if (typeof encoded !== 'string') throw new Error('release signing key is not trusted');
  const rawKey = Buffer.from(encoded, 'base64');
  if (rawKey.length !== 32) throw new Error('trusted release key has an invalid length');
  const key = crypto.createPublicKey({ key: Buffer.concat([DER_ED25519_PREFIX, rawKey]), format: 'der', type: 'spki' });
  const sig = Buffer.from(signatureDoc.signature, 'hex');
  if (sig.length !== 64 || !crypto.verify(null, rawManifest, key, sig)) throw new Error('release manifest signature is invalid');
  if (!Array.isArray(manifest.assets)) throw new Error('signed release assets must be a list');
  const seen = new Set();
  let selected;
  for (const asset of manifest.assets) {
    exactObject(asset, ['os', 'arch', 'file', 'sha256', 'binaries'], 'release asset');
    const id = `${asset.os}/${asset.arch}`;
    if (!TARGETS.has(id) || seen.has(id)) throw new Error('signed release contains an unsupported or duplicate platform');
    seen.add(id);
    if (typeof asset.file !== 'string' || asset.file !== archiveName(manifest.version, { os: asset.os, arch: asset.arch }) || path.basename(asset.file) !== asset.file) {
      throw new Error('signed release contains an unsafe archive path');
    }
    if (typeof asset.sha256 !== 'string' || !/^[a-f0-9]{64}$/.test(asset.sha256)) throw new Error('signed release contains an invalid archive digest');
    const cliName = asset.os === 'windows' ? 'ownbot.exe' : 'ownbot';
    const daemonName = asset.os === 'windows' ? 'ownbotd.exe' : 'ownbotd';
    exactObject(asset.binaries, [cliName, daemonName], 'release binary digests');
    if (![asset.binaries[cliName], asset.binaries[daemonName]].every(value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value))) {
      throw new Error('signed release contains invalid binary digests');
    }
    if (asset.os === target.os && asset.arch === target.arch) selected = asset;
  }
  if (!selected) throw new Error(`signed release does not include ${target.os}/${target.arch}`);
  return { manifest, asset: selected };
}

function fetchBuffer(rawURL, limit, redirects = 0) {
  return new Promise((resolve, reject) => {
    let url;
    try { url = new URL(rawURL); } catch { reject(new Error('invalid GitHub release URL')); return; }
    if (url.protocol !== 'https:' || !HOSTS.has(url.hostname) || (url.port && url.port !== '443') || url.username || url.password || redirects > 4) {
      reject(new Error('refusing release download outside GitHub HTTPS')); return;
    }
    const request = https.get(url, { headers: { 'User-Agent': 'OwnBot npm signed installer', Accept: 'application/vnd.github+json' } }, response => {
      if ([301, 302, 303, 307, 308].includes(response.statusCode)) {
        const location = response.headers.location;
        response.resume();
        if (!location) { reject(new Error('GitHub release redirect omitted its target')); return; }
        resolve(fetchBuffer(new URL(location, url).toString(), limit, redirects + 1));
        return;
      }
      if (response.statusCode !== 200) {
        response.resume();
        reject(new Error(`GitHub release download returned HTTP ${response.statusCode}`));
        return;
      }
      const chunks = [];
      let size = 0;
      response.on('data', chunk => {
        size += chunk.length;
        if (size > limit) {
          request.destroy(new Error('GitHub release asset exceeded its size limit'));
          return;
        }
        chunks.push(chunk);
      });
      response.on('end', () => resolve(Buffer.concat(chunks)));
      response.on('error', reject);
    });
    request.setTimeout(120000, () => request.destroy(new Error('GitHub release download timed out')));
    request.on('error', reject);
  });
}

function octalField(header, start, length, label) {
  const field = header.subarray(start, start + length).toString('ascii').replace(/\0.*$/, '').trim();
  if (!/^[0-7]+$/.test(field)) throw new Error(`release tar ${label} field is invalid`);
  return Number.parseInt(field, 8);
}

function extractTarGz(compressed, target) {
  if (compressed.length === 0 || compressed.length > MAX_ARCHIVE_BYTES) throw new Error('release archive exceeded its size limit');
  let tar;
  try { tar = zlib.gunzipSync(compressed, { maxOutputLength: MAX_ARCHIVE_BYTES }); } catch (error) { throw new Error(`release archive is not a bounded gzip tar: ${error.message}`); }
  const expected = new Set([`ownbot${target.os === 'windows' ? '.exe' : ''}`, `ownbotd${target.os === 'windows' ? '.exe' : ''}`, 'README.md']);
  const found = new Map();
  const seen = new Set();
  let offset = 0;
  while (offset + 512 <= tar.length) {
    const header = tar.subarray(offset, offset + 512);
    if (header.every(byte => byte === 0)) break;
    const name = header.subarray(0, 100).toString('utf8').replace(/\0.*$/, '');
    const prefix = header.subarray(345, 500).toString('utf8').replace(/\0.*$/, '');
    const fullName = prefix ? `${prefix}/${name}` : name;
    if (!name || fullName !== name || name.includes('/') || name.includes('\\') || name === '.' || name === '..') throw new Error('release archive contains an unsafe path');
    const savedChecksum = octalField(header, 148, 8, 'checksum');
    let checksum = 0;
    for (let i = 0; i < 512; i++) checksum += i >= 148 && i < 156 ? 32 : header[i];
    if (checksum !== savedChecksum) throw new Error('release archive header checksum is invalid');
    const size = octalField(header, 124, 12, 'size');
    const type = header[156];
    if (type !== 0 && type !== 48) throw new Error('release archive contains a non-regular file');
    if (!expected.has(name) || seen.has(name)) throw new Error('release archive contains an unexpected or duplicate file');
    seen.add(name);
    const limit = name === 'README.md' ? MAX_README_BYTES : MAX_BINARY_BYTES;
    if (size > limit || offset + 512 + size > tar.length) throw new Error('release archive file size is invalid');
    const body = Buffer.from(tar.subarray(offset + 512, offset + 512 + size));
    if (name !== 'README.md') found.set(name, body);
    offset += 512 + Math.ceil(size / 512) * 512;
  }
  const trailing = tar.subarray(offset);
  if (trailing.some(byte => byte !== 0)) throw new Error('release archive has unexpected trailing data');
  for (const name of expected) if (name !== 'README.md' && !found.has(name)) throw new Error('release archive is missing a platform binary');
  return found;
}

function installBinaries(root, version, binaries, metadata) {
  if (!/^[0-9A-Za-z.+-]+$/.test(version)) throw new Error('release version is invalid');
  const bin = path.join(root, 'bin');
  const releases = path.join(bin, 'releases');
  fs.mkdirSync(releases, { recursive: true, mode: 0o700 });
  for (const directory of [bin, releases]) {
    const info = fs.lstatSync(directory);
    if (!info.isDirectory() || info.isSymbolicLink()) throw new Error(`release install path is not a real directory: ${directory}`);
  }
  const final = path.join(releases, version);
  if (fs.existsSync(final)) {
    const info = fs.lstatSync(final);
    if (!info.isDirectory() || info.isSymbolicLink()) throw new Error(`release version path is not a real directory: ${final}`);
    for (const [name, bytes] of binaries) {
      const existing = path.join(final, name);
      if (!fs.lstatSync(existing).isFile() || !fs.readFileSync(existing).equals(bytes)) throw new Error(`version directory contains unexpected data: ${final}`);
    }
    if (metadata) {
      for (const name of ['manifest.json', 'manifest.sig']) {
        const existing = path.join(final, name);
        if (!fs.lstatSync(existing).isFile() || !fs.readFileSync(existing).equals(metadata[name])) throw new Error(`version directory is missing signed release metadata: ${final}`);
      }
    }
  } else {
    const stage = fs.mkdtempSync(path.join(releases, '.stage-'));
    try {
      for (const [name, bytes] of binaries) fs.writeFileSync(path.join(stage, name), bytes, { mode: 0o755, flag: 'wx' });
      if (metadata) {
        for (const name of ['manifest.json', 'manifest.sig']) fs.writeFileSync(path.join(stage, name), metadata[name], { mode: 0o644, flag: 'wx' });
      }
      fs.renameSync(stage, final);
    } catch (error) {
      fs.rmSync(stage, { recursive: true, force: true });
      throw error;
    }
  }
  const marker = path.join(bin, 'current.json');
  const temp = path.join(bin, `.current-${process.pid}-${Date.now()}.tmp`);
  fs.writeFileSync(temp, `${JSON.stringify({ version })}\n`, { mode: 0o600, flag: 'wx' });
  fs.renameSync(temp, marker);
}

async function installPackageVersion(root, packageVersion, options = {}) {
  const trustPath = path.join(root, 'internal', 'update', 'trusted-keys.json');
  const keys = options.keys || JSON.parse(fs.readFileSync(trustPath, 'utf8'));
  if (!Object.keys(keys).length) throw new Error('signed prebuilt install is disabled until maintainers pin the release public key in internal/update/trusted-keys.json; use OWNBOT_BUILD_FROM_SOURCE=1 for an explicit Go source build');
  const target = options.target || targetFor();
  const fetch = options.fetchBuffer || fetchBuffer;
  const version = packageVersion.replace(/^v/, '');
  const releaseURL = `https://api.github.com/repos/${REPOSITORY}/releases/tags/v${encodeURIComponent(version)}`;
  const release = JSON.parse((await fetch(releaseURL, MAX_JSON_BYTES)).toString('utf8'));
  if (release.tag_name !== `v${version}` || !Array.isArray(release.assets)) throw new Error('GitHub release tag does not match this npm package version');
  const assetURL = name => {
    const asset = release.assets.find(item => item.name === name);
    return asset && asset.browser_download_url;
  };
  const manifestURL = assetURL('manifest.json');
  const signatureURL = assetURL('manifest.sig');
  if (!manifestURL || !signatureURL) throw new Error('GitHub release is missing its signed manifest');
  const manifest = await fetch(manifestURL, MAX_JSON_BYTES);
  const signature = await fetch(signatureURL, MAX_SIGNATURE_BYTES);
  const verified = verifyManifest(manifest, signature, keys, target, version);
  const archiveURL = assetURL(verified.asset.file);
  if (!archiveURL) throw new Error('GitHub release is missing the signed platform archive');
  const archive = await fetch(archiveURL, MAX_ARCHIVE_BYTES);
  const digest = crypto.createHash('sha256').update(archive).digest('hex');
  if (digest !== verified.asset.sha256) throw new Error('release archive digest does not match the signed manifest');
  const binaries = extractTarGz(archive, target);
  for (const [name, bytes] of binaries) {
    if (crypto.createHash('sha256').update(bytes).digest('hex') !== verified.asset.binaries[name]) throw new Error(`release binary ${name} does not match its signed digest`);
  }
  installBinaries(root, version, binaries, { 'manifest.json': manifest, 'manifest.sig': signature });
}

module.exports = { archiveName, extractTarGz, installBinaries, installPackageVersion, targetFor, verifyManifest };
