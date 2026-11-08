import OpenCC from 'opencc-js/t2cn';
import { pathToFileURL } from 'node:url';

const tw2s = OpenCC.Converter({ from: 'tw', to: 'cn' });
const maxBytes = 8 << 20;

export function convertText(input, convert = tw2s) {
  if (!input || typeof input.text !== 'string' || !Array.isArray(input.boundaries)) throw new Error('invalid input');
  const chars = Array.from(input.text);
  if (chars.length > 4096 || input.boundaries.length > 128 || input.boundaries.some(n => !Number.isInteger(n) || n < 0 || n > chars.length)) throw new Error('invalid boundary');
  const text = convert(input.text);
  if (Array.from(text).length > 4096) throw new Error('output limit');
  const boundaries = input.boundaries.map(n => {
    const left = convert(chars.slice(0, n).join(''));
    const right = convert(chars.slice(n).join(''));
    // Never infer style/page offsets from equal string lengths. A context-
    // dependent split must be reviewed rather than silently changing anchors.
    if (left + right !== text) throw new Error('ambiguous conversion boundary');
    return Array.from(left).length;
  });
  return { text, boundaries };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const chunks = [];
    let size = 0;
    for await (const chunk of process.stdin) {
      size += chunk.length;
      if (size > maxBytes) throw new Error('input limit');
      chunks.push(chunk);
    }
    const input = JSON.parse(Buffer.concat(chunks).toString('utf8'));
    if (!Array.isArray(input) || input.length > 50002) throw new Error('input limit');
    const output = JSON.stringify(input.map(item => convertText(item)));
    if (Buffer.byteLength(output) > maxBytes) throw new Error('output limit');
    process.stdout.write(output);
  } catch {
    // Do not leak member content into worker logs.
    process.stderr.write('simplified_conversion_failed\n');
    process.exitCode = 1;
  }
}
