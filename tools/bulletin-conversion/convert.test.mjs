import assert from 'node:assert/strict';
import { test } from 'node:test';

import { convertText } from './convert.mjs';

test('whole sentence conversion keeps cross-span context and Unicode offsets', () => {
  assert.deepEqual(convertText({ text: '乾杯😀，神的話語。', boundaries: [0, 2, 3, 9] }), {
    text: '干杯😀，神的话语。', boundaries: [0, 2, 3, 9],
  });
});

test('retains names and scripture wording without regional vocabulary replacement', () => {
  assert.deepEqual(convertText({ text: '乾隆使用滑鼠，神愛世人。', boundaries: [0, 2, 12] }), {
    text: '乾隆使用滑鼠，神爱世人。', boundaries: [0, 2, 12],
  });
});

test('rejects ambiguous style boundaries rather than corrupting a name', () => {
  assert.throws(() => convertText({ text: '乾隆', boundaries: [0, 1, 2] }), /boundary/);
});

test('handles length-changing conversion at a proven boundary', () => {
  // Injectable pure converter exercises the alignment policy independently of
  // whether this pinned dictionary currently contains a length-changing entry.
  assert.deepEqual(convertText({ text: '甲乙丙', boundaries: [0, 2, 3] }, text => text.replace('甲乙', '丁')), {
    text: '丁丙', boundaries: [0, 1, 2],
  });
});

test('rejects invalid code point boundaries and oversized text', () => {
  for (const input of [{ text: '😀', boundaries: [2] }, { text: '甲', boundaries: [-1] }, { text: '甲', boundaries: [0.5] }, { text: '甲'.repeat(4097), boundaries: [0] }]) {
    assert.throws(() => convertText(input));
  }
});
