import {open} from 'node:fs/promises';
import {fileURLToPath, pathToFileURL} from 'node:url';

export async function composeWithDiagnostics(input, renderer) {
  try {
    return await renderer.composeBulletinLayout(input);
  } catch (error) {
    if (!['page_requires_edit','cover_requires_edit'].includes(error.message)) throw error;
    const validID = id => typeof id === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(id);
    const pageId = error.cause?.pageId ?? (error.message === 'cover_requires_edit' ? JSON.parse(input.submissionJSON).document.layoutManifest.pages[0]?.pageId : undefined);
    if (pageId !== undefined && !validID(pageId) || error.cause?.slotId !== undefined && !validID(error.cause.slotId)) throw error;
    let evidence;
    if (error.cause?.slotId && pageId) evidence = [error.cause];
    else {
      evidence = (await renderer.measureBulletinLayout(input)).overflow;
      if (!Array.isArray(evidence)) throw error;
      if (pageId) evidence = evidence.filter(location => location.pageId === pageId);
      if (!evidence.length && pageId) evidence = [{pageId}];
    }
    if (!Array.isArray(evidence) || !evidence.length || evidence.length > 64) throw error;
    const locations = [];
    for (const {pageId, slotId} of evidence) {
      if (!validID(pageId) || slotId !== undefined && !validID(slotId)) throw error;
      if (!locations.some(location => location.pageId === pageId && location.slotId === slotId)) locations.push(slotId ? {pageId, slotId} : {pageId});
    }
    return {failure: {
      contentHash: input.expectedContentHash,
      rendererArtifactSha256: JSON.parse(input.submissionJSON).document.layoutManifest.rendererArtifactSha256,
      locations,
    }};
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    const [modulePath, inputPath, assetsDirectory, ...extra] = process.argv.slice(2);
    if (extra.length || !inputPath || !assetsDirectory || !/^\/opt\/bulletin-renderer\/scripts\/measure-bulletin-layout(?:-v(?:[2-9]|10|11))?\.mjs$/.test(modulePath)) throw new Error('invalid_arguments');
    const file = await open(inputPath, 'r');
    let input;
    try {
      if (!(await file.stat()).isFile()) throw new Error('invalid_input');
      const bytes = Buffer.alloc(16 * 1024 * 1024 + 1);
      const {bytesRead} = await file.read(bytes, 0, bytes.length, 0);
      if (bytesRead === bytes.length) throw new Error('input_too_large');
      input = JSON.parse(bytes.subarray(0, bytesRead).toString('utf8'));
    } finally { await file.close(); }
    const result = await composeWithDiagnostics({...input, assetsDirectory}, await import(pathToFileURL(modulePath).href));
    process.stdout.write(JSON.stringify(result) + '\n');
    if (result.failure) process.exitCode = 1;
  } catch {
    process.stderr.write('layout_runner_failed\n');
    process.exitCode = 1;
  }
}
