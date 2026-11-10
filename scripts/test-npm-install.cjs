#!/usr/bin/env node
'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const http = require('node:http');
const os = require('node:os');
const path = require('node:path');
const { spawn, spawnSync } = require('node:child_process');

const root = path.join(__dirname, '..');
const metadata = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8'));
const npmCLI = process.env.npm_execpath;
if (!npmCLI) throw new Error('run this smoke test with `npm run test:install` so npm can provide its CLI path');

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { encoding: 'utf8', ...options });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${command} ${args.join(' ')} failed (${result.status})\n${result.stdout}\n${result.stderr}`);
  return result;
}

function runAsync(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { ...options, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '';
    let stderr = '';
    child.stdout.setEncoding('utf8').on('data', (chunk) => { stdout += chunk; });
    child.stderr.setEncoding('utf8').on('data', (chunk) => { stderr += chunk; });
    child.on('error', reject);
    child.on('close', (status) => {
      if (status === 0) resolve({ stdout, stderr });
      else reject(new Error(`${command} ${args.join(' ')} failed (${status})\n${stdout}\n${stderr}`));
    });
  });
}

function hash(bytes) {
  return crypto.createHash('sha256').update(bytes).digest('hex');
}

function fixtureArchive(stage, destination, win) {
  if (win) {
    const script = "Add-Type -AssemblyName System.IO.Compression.FileSystem; [System.IO.Compression.ZipFile]::CreateFromDirectory($env:OWNBOT_FIXTURE_DIR, $env:OWNBOT_FIXTURE_ARCHIVE)";
    run('powershell.exe', ['-NoLogo', '-NoProfile', '-NonInteractive', '-Command', script], {
      env: { ...process.env, OWNBOT_FIXTURE_DIR: stage, OWNBOT_FIXTURE_ARCHIVE: destination },
    });
  } else {
    run('tar', ['-czf', destination, '-C', stage, 'ownbot', 'ownbotd']);
  }
}

async function main() {
  const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'ownbot-npm-smoke-'));
  let server;
  try {
    const nodeDirectory = path.dirname(process.execPath);
    const windows = process.platform === 'win32';
    const osName = windows ? 'windows' : process.platform;
    const arch = process.arch === 'x64' ? 'amd64' : process.arch;
    const suffix = windows ? '.exe' : '';
    const ext = windows ? '.zip' : '.tar.gz';
    const name = `ownbot-${metadata.version}-${osName}-${arch}${ext}`;
    const stage = path.join(temp, 'stage');
    fs.mkdirSync(stage);
    for (const binary of ['ownbot', 'ownbotd']) {
      const body = windows ? 'fixture binary\r\n' : '#!/bin/sh\necho ownbot-smoke-ok\n';
      const padded = body + '#'.repeat(Math.max(1100 - body.length, 0));
      fs.writeFileSync(path.join(stage, binary + suffix), padded, { mode: 0o755 });
    }
    const archivePath = path.join(temp, name);
    fixtureArchive(stage, archivePath, windows);
    const archive = fs.readFileSync(archivePath);
    const manifest = Buffer.from(JSON.stringify({
      name: 'ownbot',
      version: metadata.version,
      assets: [{ os: osName, arch, file: name, sha256: hash(archive) }],
    }, null, 2) + '\n');
    const sums = Buffer.from(`${hash(manifest)}  manifest.json\n${hash(archive)}  ${name}\n`);
    const files = new Map([['manifest.json', manifest], ['SHA256SUMS', sums], [name, archive]]);
    server = http.createServer((request, response) => {
      const file = files.get(path.basename(new URL(request.url, 'http://localhost').pathname));
      if (!file) { response.writeHead(404).end('not found'); return; }
      response.writeHead(200, { 'Content-Length': file.length });
      response.end(file);
    });
    await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
    const address = server.address();
    const baseURL = `http://127.0.0.1:${address.port}/v${metadata.version}`;

    const packDir = path.join(temp, 'pack');
    fs.mkdirSync(packDir);
    const noGoPath = [nodeDirectory, windows ? 'C:\\Windows\\System32' : '/usr/bin', windows ? 'C:\\Windows\\System32\\WindowsPowerShell\\v1.0' : '/bin'].join(path.delimiter);
    const env = { ...process.env, PATH: noGoPath, OWNBOT_RELEASE_BASE_URL: baseURL };
    const npm = (args) => run(process.execPath, [npmCLI, ...args], { cwd: root, env, maxBuffer: 8 * 1024 * 1024 });
    npm(['pack', '--ignore-scripts', '--pack-destination', packDir, '--silent']);
    const packed = path.join(packDir, `ownbot-${metadata.version}.tgz`);
    assert.ok(fs.statSync(packed).size > 0, 'npm pack should produce an installable tarball');

    const installPrefix = path.join(temp, 'install');
    const installResult = await runAsync(process.execPath, [npmCLI, 'install', '--prefix', installPrefix, '--no-audit', '--no-fund', '--package-lock=false', '--ignore-scripts=false', packed], { cwd: root, env, maxBuffer: 8 * 1024 * 1024 });
    if (installResult.stdout.trim()) process.stdout.write(installResult.stdout);
    if (installResult.stderr.trim()) process.stderr.write(installResult.stderr);
    const packageDir = path.join(installPrefix, 'node_modules', 'ownbot');
    const cli = path.join(packageDir, 'bin', `ownbot${suffix}`);
    const daemon = path.join(packageDir, 'bin', `ownbotd${suffix}`);
    assert.ok(fs.statSync(cli).size >= 1024, 'install should unpack the CLI binary');
    assert.ok(fs.statSync(daemon).size >= 1024, 'install should unpack the daemon binary');
    if (!windows) {
      assert.ok((fs.statSync(cli).mode & 0o111) !== 0, 'CLI binary should have executable permissions');
      assert.ok((fs.statSync(daemon).mode & 0o111) !== 0, 'daemon binary should have executable permissions');
      const launched = run(process.execPath, [path.join(packageDir, 'scripts', 'ownbot.js'), '--help'], { cwd: root, env });
      assert.match(launched.stdout, /ownbot-smoke-ok/, 'installed wrapper should launch the prebuilt CLI');
    }
    console.log(`PASS: npm pack install used the verified ${osName}/${arch} archive without Go`);
  } finally {
    if (server) await new Promise((resolve) => server.close(resolve));
    fs.rmSync(temp, { recursive: true, force: true });
  }
}

main().catch((error) => { console.error(error.stack || error); process.exitCode = 1; });
