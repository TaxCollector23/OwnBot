'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const zlib = require('node:zlib');
const { extractTarGz, installBinaries, installPackageVersion, verifyManifest } = require('./release-install');

function signedManifest(t) {
  const pair = crypto.generateKeyPairSync('ed25519');
  const rawPublic = pair.publicKey.export({ format: 'der', type: 'spki' }).subarray(-32);
  const assets = [
    ['darwin', 'arm64'], ['darwin', 'amd64'], ['linux', 'arm64'],
    ['linux', 'amd64'], ['windows', 'amd64'], ['freebsd', 'amd64'],
  ].map(([osName, arch]) => ({
    os: osName,
    arch,
    file: `ownbot-1.2.3-${osName}-${arch}.tar.gz`,
    sha256: 'a'.repeat(64),
    binaries: osName === 'windows' ? { 'ownbot.exe': 'a'.repeat(64), 'ownbotd.exe': 'a'.repeat(64) } : { ownbot: 'a'.repeat(64), ownbotd: 'a'.repeat(64) },
  }));
  const raw = Buffer.from(JSON.stringify({ name: 'ownbot', version: '1.2.3', assets }));
  const sig = crypto.sign(null, raw, pair.privateKey).toString('hex');
  t.keys = { 'fixture-test-key': rawPublic.toString('base64') };
  t.raw = raw;
  t.signature = Buffer.from(JSON.stringify({ key_id: 'fixture-test-key', signature: sig }));
}

function tarGz(entries) {
  const blocks = [];
  for (const { name, body } of entries) {
    const header = Buffer.alloc(512);
    header.write(name, 0, 100, 'utf8');
    header.write('0000755\0', 100, 8, 'ascii');
    header.write('0000000\0', 108, 8, 'ascii');
    header.write('0000000\0', 116, 8, 'ascii');
    header.write(`${body.length.toString(8).padStart(11, '0')}\0`, 124, 12, 'ascii');
    header.write('00000000000\0', 136, 12, 'ascii');
    header.fill(0x20, 148, 156);
    header[156] = 0x30;
    header.write('ustar\0', 257, 6, 'ascii');
    header.write('00', 263, 2, 'ascii');
    let checksum = 0;
    for (const byte of header) checksum += byte;
    header.write(`${checksum.toString(8).padStart(6, '0')}\0 `, 148, 8, 'ascii');
    blocks.push(header, body);
    if (body.length % 512) blocks.push(Buffer.alloc(512 - (body.length % 512)));
  }
  blocks.push(Buffer.alloc(1024));
  return zlib.gzipSync(Buffer.concat(blocks));
}

test('Node verifies signed release and rejects tamper, wrong version, platform, and paths', t => {
  signedManifest(t);
  const target = { os: 'linux', arch: 'amd64' };
  assert.equal(verifyManifest(t.raw, t.signature, t.keys, target, '1.2.3').asset.os, 'linux');
  assert.throws(() => verifyManifest(Buffer.concat([t.raw, Buffer.from(' ')]), t.signature, t.keys, target, '1.2.3'), /signature/);
  assert.throws(() => verifyManifest(t.raw, t.signature, t.keys, target, '1.2.4'), /version/);
  assert.throws(() => verifyManifest(t.raw, t.signature, t.keys, { os: 'freebsd', arch: 'arm64' }, '1.2.3'), /does not include/);

  const manifest = JSON.parse(t.raw.toString());
  manifest.assets[3].file = '../escape.tar.gz';
  const raw = Buffer.from(JSON.stringify(manifest));
  // Even a trusted-key-signed malformed manifest is rejected by path validation.
  const privatePair = crypto.generateKeyPairSync('ed25519');
  t.keys = { 'fixture-test-key': privatePair.publicKey.export({ format: 'der', type: 'spki' }).subarray(-32).toString('base64') };
  const signedBad = Buffer.from(JSON.stringify({ key_id: 'fixture-test-key', signature: crypto.sign(null, raw, privatePair.privateKey).toString('hex') }));
  assert.throws(() => verifyManifest(raw, signedBad, t.keys, target, '1.2.3'), /path/);
});

test('Node bounded tar reader rejects traversal and accepts only expected binaries', () => {
  const target = { os: 'linux', arch: 'amd64' };
  const archive = tarGz([{ name: 'ownbot', body: Buffer.from('cli') }, { name: 'ownbotd', body: Buffer.from('daemon') }, { name: 'README.md', body: Buffer.from('readme') }]);
  const files = extractTarGz(archive, target);
  assert.equal(files.get('ownbot').toString(), 'cli');
  assert.equal(files.get('ownbotd').toString(), 'daemon');
  assert.throws(() => extractTarGz(tarGz([{ name: '../escape', body: Buffer.from('x') }]), target), /unsafe path/);
  assert.throws(() => extractTarGz(tarGz([{ name: 'ownbot', body: Buffer.from('x') }]), target), /missing/);
});

test('Node install activates versioned binaries and preserves current files on same-version conflict', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'ownbot-npm-install-'));
  try {
    const binaries = new Map([['ownbot', Buffer.from('cli')], ['ownbotd', Buffer.from('daemon')]]);
    installBinaries(root, '1.2.3', binaries);
    const release = path.join(root, 'bin', 'releases', '1.2.3');
    assert.equal(fs.readFileSync(path.join(release, 'ownbot'), 'utf8'), 'cli');
    assert.deepEqual(JSON.parse(fs.readFileSync(path.join(root, 'bin', 'current.json'), 'utf8')), { version: '1.2.3' });
    assert.throws(() => installBinaries(root, '1.2.3', new Map([['ownbot', Buffer.from('changed')], ['ownbotd', Buffer.from('daemon')]])), /unexpected data/);
    assert.equal(fs.readFileSync(path.join(release, 'ownbot'), 'utf8'), 'cli');
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

test('npm installer verifies a signed platform archive before activating it', async t => {
  const pair = crypto.generateKeyPairSync('ed25519');
  const publicKey = pair.publicKey.export({ format: 'der', type: 'spki' }).subarray(-32);
  const target = { os: 'linux', arch: 'amd64' };
  const archive = tarGz([
    { name: 'ownbot', body: Buffer.from('signed cli') },
    { name: 'ownbotd', body: Buffer.from('signed daemon') },
    { name: 'README.md', body: Buffer.from('fixture') },
  ]);
  const digest = crypto.createHash('sha256').update(archive).digest('hex');
  const assets = [
    ['darwin', 'arm64'], ['darwin', 'amd64'], ['linux', 'arm64'],
    ['linux', 'amd64'], ['windows', 'amd64'], ['freebsd', 'amd64'],
  ].map(([osName, arch]) => ({
    os: osName,
    arch,
    file: `ownbot-1.2.3-${osName}-${arch}.tar.gz`,
    sha256: 'b'.repeat(64),
    binaries: osName === 'windows' ? { 'ownbot.exe': 'c'.repeat(64), 'ownbotd.exe': 'c'.repeat(64) } : { ownbot: 'c'.repeat(64), ownbotd: 'c'.repeat(64) },
  }));
  assets.find(asset => asset.os === target.os && asset.arch === target.arch).sha256 = digest;
  assets.find(asset => asset.os === target.os && asset.arch === target.arch).binaries = {
    ownbot: crypto.createHash('sha256').update('signed cli').digest('hex'),
    ownbotd: crypto.createHash('sha256').update('signed daemon').digest('hex'),
  };
  const rawManifest = Buffer.from(JSON.stringify({ name: 'ownbot', version: '1.2.3', assets }));
  const signature = Buffer.from(JSON.stringify({ key_id: 'fixture-test-key', signature: crypto.sign(null, rawManifest, pair.privateKey).toString('hex') }));
  const release = {
    tag_name: 'v1.2.3',
    assets: ['manifest.json', 'manifest.sig', assets[3].file].map(name => ({ name, browser_download_url: `https://github.com/fixtures/${name}` })),
  };
  const responses = new Map([
    ['https://api.github.com/repos/TaxCollector23/OwnBot/releases/tags/v1.2.3', Buffer.from(JSON.stringify(release))],
    ['https://github.com/fixtures/manifest.json', rawManifest],
    ['https://github.com/fixtures/manifest.sig', signature],
    [`https://github.com/fixtures/${assets[3].file}`, archive],
  ]);
  const fetch = async url => {
    if (!responses.has(url)) throw new Error(`unexpected URL ${url}`);
    return responses.get(url);
  };
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'ownbot-signed-npm-'));
  try {
    await installPackageVersion(root, '1.2.3', { keys: { 'fixture-test-key': publicKey.toString('base64') }, target, fetchBuffer: fetch });
    const active = JSON.parse(fs.readFileSync(path.join(root, 'bin', 'current.json'), 'utf8'));
    assert.equal(active.version, '1.2.3');
    const versionDir = path.join(root, 'bin', 'releases', '1.2.3');
    assert.equal(fs.readFileSync(path.join(versionDir, 'ownbot'), 'utf8'), 'signed cli');
    assert.deepEqual(fs.readFileSync(path.join(versionDir, 'manifest.json')), rawManifest);
    assert.deepEqual(fs.readFileSync(path.join(versionDir, 'manifest.sig')), signature);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});
