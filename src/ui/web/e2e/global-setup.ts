// Builds the UI, starts the real core with a simulated guest
// (`go run ./tools/devserver -port 0`) and exposes its endpoints to the tests.
import { execSync, spawn, type ChildProcess } from 'node:child_process';
import { createInterface } from 'node:readline';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const webDir = path.resolve(here, '..');
const repoRoot = path.resolve(webDir, '../../..');

export interface DevInfo {
  url: string;
  base: string;
  token: string;
  dev: string;
}

function withToolPath(env: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const extra = ['/opt/homebrew/bin', '/usr/local/go/bin', '/usr/local/bin'];
  return { ...env, PATH: [...extra, env.PATH ?? ''].join(path.delimiter) };
}

function waitForInfo(child: ChildProcess, timeoutMs: number): Promise<DevInfo> {
  return new Promise((resolve, reject) => {
    let stderr = '';
    const timer = setTimeout(() => reject(new Error(`devserver did not start within ${timeoutMs} ms\n${stderr}`)), timeoutMs);
    child.stderr?.on('data', (d: Buffer) => {
      stderr = (stderr + d.toString()).slice(-8000);
    });
    child.once('exit', (code) => {
      clearTimeout(timer);
      reject(new Error(`devserver exited early (code ${code})\n${stderr}`));
    });
    const rl = createInterface({ input: child.stdout! });
    rl.on('line', (line) => {
      try {
        const info = JSON.parse(line) as Partial<DevInfo>;
        if (info.url && info.token && info.dev && info.base) {
          clearTimeout(timer);
          resolve(info as DevInfo);
        }
      } catch {
        // not the info line
      }
    });
  });
}

export default async function globalSetup(): Promise<() => Promise<void>> {
  if (!process.env.E2E_SKIP_BUILD) {
    execSync('npm run build', { cwd: webDir, stdio: 'inherit', env: withToolPath(process.env) });
  }
  const child = spawn('go', ['run', './tools/devserver', '-port', '0'], {
    cwd: repoRoot,
    env: withToolPath(process.env),
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true, // own process group: teardown stops `go run` and the server binary together
  });
  const info = await waitForInfo(child, 300_000);
  process.env.E2E_URL = info.url;
  process.env.E2E_BASE = info.base;
  process.env.E2E_TOKEN = info.token;
  process.env.E2E_DEV = info.dev;

  return async () => {
    const pid = child.pid;
    if (!pid) return;
    const exited = new Promise<void>((r) => child.once('exit', () => r()));
    try {
      process.kill(-pid, 'SIGINT');
    } catch {
      return;
    }
    const timeout = new Promise<'timeout'>((r) => setTimeout(() => r('timeout'), 10_000));
    if ((await Promise.race([exited, timeout])) === 'timeout') {
      try {
        process.kill(-pid, 'SIGKILL');
      } catch {
        // already gone
      }
    }
  };
}
