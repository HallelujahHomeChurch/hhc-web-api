import {createHash} from 'node:crypto';
import {lstatSync, readFileSync, readdirSync, readlinkSync} from 'node:fs';
import {join} from 'node:path';

// ldconfig's builder-only auxiliary cache is distinct from the hashed /etc/ld.so.cache.
const excluded = new Set(['/proc', '/sys', '/dev', '/tmp', '/run', '/var/log', '/var/cache/ldconfig/aux-cache', '/etc/hosts', '/etc/hostname', '/etc/resolv.conf', '/.dockerenv']);
const hash = createHash('sha256');
function visit(path) {
  if (excluded.has(path)) return;
  const stat = lstatSync(path);
  hash.update(JSON.stringify([path, stat.mode & 0o7777, stat.uid, stat.gid]));
  if (stat.isSymbolicLink()) hash.update(readlinkSync(path));
  else if (stat.isDirectory()) for (const name of readdirSync(path).sort()) visit(join(path, name));
  else if (stat.isFile()) {
    let bytes = readFileSync(path);
    // Fontconfig 2.15 FcCache v9 LE64: only directory mtime/checksum occupy bytes 48..63.
    // Keep every pattern and offset; unknown formats must not reuse verification evidence.
    if (path.startsWith('/var/cache/fontconfig/') && /\.cache-/.test(path)) {
      if (!/-le64\.cache-9$/.test(path) || bytes.length < 64 ||
          bytes.readUInt32LE(0) !== 0xfc02fc04 || bytes.readUInt32LE(4) !== 9 ||
          bytes.readBigInt64LE(8) !== BigInt(bytes.length)) throw new Error('unsupported font cache');
      bytes.fill(0, 48, 64);
    }
    // The immutable renderer verifier also excludes the UI package's release version.
    if (path === '/opt/bulletin-renderer/packages/ui/package.json') {
      const metadata = JSON.parse(bytes); delete metadata.version;
      bytes = Buffer.from(JSON.stringify(metadata));
    }
    hash.update(createHash('sha256').update(bytes).digest());
  }
  else throw new Error(`unsupported image entry: ${path}`);
}
visit('/');
console.log(hash.digest('hex'));
