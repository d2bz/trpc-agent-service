import { spawnSync } from 'node:child_process';
import { loadEnvFile } from 'node:process';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
try {
  loadEnvFile(resolve(root, process.argv[2] ?? 'data/local.env'));
} catch {
  console.error('Cannot load local environment file; check its path and permissions.');
  process.exit(1);
}

const result = spawnSync('bash', ['./start.sh'], { cwd: root, stdio: 'inherit' });
if (result.error) console.error('Cannot execute start.sh.');
process.exit(result.status ?? 1);
