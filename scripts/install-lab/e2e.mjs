#!/usr/bin/env node
// Disposable npm install + CLI/SSH owner-bootstrap integration check.
// Uses only a fresh temp directory and an OS-assigned loopback port.
import { chmod, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import net from 'node:net';

const repo = resolve(new URL('../..', import.meta.url).pathname);
const temp = await mkdtemp(join(tmpdir(), 'ownbot-install-lab-'));
let ownbot;
let url;
let dataDir = join(temp, 'state');
let started = false;
function run(bin, args, options = {}) {
  const result = spawnSync(bin, args, { encoding: 'utf8', timeout: 120_000, ...options });
  if (result.error || result.status !== 0) {
    throw new Error(`${bin} ${args[0] ?? ''} failed (status ${result.status ?? 'spawn error'})`);
  }
  return { stdout: result.stdout ?? '', stderr: result.stderr ?? '' };
}
async function freePort() {
  const server = net.createServer();
  await new Promise((resolve, reject) => server.listen(0, '127.0.0.1', resolve).once('error', reject));
  const { port } = server.address();
  await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  return port;
}
try {
  const pack = run('npm', ['pack', '--pack-destination', temp], { cwd: repo });
  const tarName = pack.stdout.trim().split('\n').at(-1);
  const prefix = join(temp, 'prefix');
  run('npm', ['install', '--prefix', prefix, '--no-audit', '--no-fund', join(temp, tarName)], {
    env: { ...process.env, OWNBOT_BUILD_FROM_SOURCE: '1' },
  });
  ownbot = join(prefix, 'node_modules', '.bin', 'ownbot');
  run(ownbot, ['--help']);
  const packageRoot = join(prefix, 'node_modules', 'ownbot');
  const active = JSON.parse(await readFile(join(packageRoot, 'bin', 'current.json'), 'utf8'));
  const daemon = join(packageRoot, 'bin', 'releases', active.version, process.platform === 'win32' ? 'ownbotd.exe' : 'ownbotd');
  run(daemon, ['--help']);
  const port = await freePort();
  url = `http://127.0.0.1:${port}`;
  const env = { ...process.env, SSH_CONNECTION: '127.0.0.1 2222 127.0.0.1 22', OWNBOT_DATA_DIR: dataDir };
  const start = run(ownbot, ['daemon', 'start', '--data-dir', dataDir, '--listen', `127.0.0.1:${port}`], { env });
  started = true;
  const output = start.stdout + start.stderr;
  const link = output.match(/https?:\/\/127\.0\.0\.1:\d+\/dashboard#bootstrap=[A-Za-z0-9_-]+/u)?.[0];
  if (!output.includes(`ssh -N -L ${port}:127.0.0.1:${port}`) || !link) throw new Error('SSH startup omitted tunnel command or one-time owner link');
  const master = (await readFile(join(dataDir, 'api.token'), 'utf8')).trim();
  if (!master || output.includes(master)) throw new Error('SSH startup output exposed the master API token');
  const ticket = new URL(link).hash.slice('#bootstrap='.length);
  const exchange = async () => fetch(`${url}/api/access/bootstrap/exchange`, {
    method: 'POST', headers: { 'Content-Type': 'application/json', Origin: url }, body: JSON.stringify({ ticket }),
  });
  const redeemed = await exchange();
  if (redeemed.status !== 201) throw new Error(`owner link redemption returned ${redeemed.status}`);
  const ownerSession = await redeemed.json();
  if (typeof ownerSession.token !== 'string' || !ownerSession.token) throw new Error('owner session token missing after redemption');
  const replay = await exchange();
  if (replay.status !== 401) throw new Error(`owner link replay returned ${replay.status}, expected 401`);
  const status = run(ownbot, ['inspect', '--url', url, '--data-dir', dataDir]);
  if (!status.stdout.includes('"service": "ownbot"')) throw new Error('installed CLI status did not identify OwnBot');
  // daemon status inspects the platform service manager when a service exists.
  // Shadow that executable so this check can never call a real service manager.
  const fakeBin = join(temp, 'fake-bin');
  await mkdir(fakeBin);
  const serviceManager = process.platform === 'darwin' ? 'launchctl' : process.platform === 'linux' ? 'systemctl' : null;
  if (serviceManager) {
    await writeFile(join(fakeBin, serviceManager), '#!/bin/sh\nexit 1\n', { mode: 0o700 });
    await chmod(join(fakeBin, serviceManager), 0o700);
  }
  const statusEnv = { ...process.env, PATH: `${fakeBin}:${process.env.PATH}`, OWNBOT_DATA_DIR: dataDir };
  const daemonStatus = run(ownbot, ['daemon', 'status', '--data-dir', dataDir, '--listen', `127.0.0.1:${port}`], { env: statusEnv });
  if (!daemonStatus.stdout.includes(`Daemon: running at ${url}`)) throw new Error('daemon status did not report the running daemon');
  const stop = run(ownbot, ['daemon', 'stop', '--url', url, '--data-dir', dataDir]);
  started = false;
  if (!stop.stdout.toLowerCase().includes('daemon stopped')) throw new Error('daemon stop did not report success');
  let unavailable = false;
  try { await fetch(`${url}/api/status`, { signal: AbortSignal.timeout(500) }); } catch { unavailable = true; }
  if (!unavailable) throw new Error('daemon still responded after stop');
  process.stdout.write('PASS npm tarball install/postinstall; ownbot and ownbotd wrappers/binaries; SSH tunnel + one-time link; master token hidden; redemption 201/replay 401; CLI inspect/daemon status/stop. Fixture: local disposable daemon, synthetic SSH environment, and service-manager stub.\n');
} finally {
  if (started && ownbot && url) {
    spawnSync(ownbot, ['daemon', 'stop', '--url', url, '--data-dir', dataDir], { encoding: 'utf8', timeout: 25_000 });
  }
  await rm(temp, { recursive: true, force: true });
}
