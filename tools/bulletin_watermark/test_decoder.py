import contextlib
import io
import json
from pathlib import Path
import struct
import tempfile
import unittest
from unittest.mock import patch

import engine
import evaluate_decoder
import worker


class DecoderContractTests(unittest.TestCase):
    def test_accepts_only_a_valid_payload_above_the_frozen_quality(self):
        payload = '01' * 30 + '0'
        with patch.object(engine, '_decode_with_quality', return_value=(payload, True, .91)):
            result = engine.decode_image(object(), object(), .90)
        self.assertEqual(result, engine.DecodeResult('decoded', payload, .91))

        for decoded, detected, quality in [
            ('1' * 60, True, .99),
            ('x' * 61, True, .99),
            (payload, False, .99),
            (payload, True, .89),
        ]:
            with self.subTest(decoded=decoded, detected=detected, quality=quality):
                with patch.object(engine, '_decode_with_quality', return_value=(decoded, detected, quality)):
                    self.assertEqual(engine.decode_image(object(), object(), .90).status, 'inconclusive')

    def test_decoder_does_not_print_a_payload(self):
        payload = '1' * 61
        stdout, stderr = io.StringIO(), io.StringIO()
        with patch.object(engine, '_decode_with_quality', return_value=(payload, True, .99)):
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                result = engine.decode_image(object(), object(), .90)
        self.assertEqual(result.payload, payload)
        self.assertNotIn(payload, stdout.getvalue() + stderr.getvalue())


class EvaluationContractTests(unittest.TestCase):
    def test_unknown_payload_is_inconclusive_and_wrong_retained_payload_is_false_attribution(self):
        retained = {'0' * 61: 'receipt-a', '1' * 61: 'receipt-b'}
        unknown = evaluate_decoder.classify('receipt-a', '01' * 30 + '0', retained)
        wrong = evaluate_decoder.classify('receipt-a', '1' * 61, retained)
        self.assertEqual(unknown, {'outcome': 'inconclusive', 'falseAttribution': False})
        self.assertEqual(wrong, {'outcome': 'matched', 'receiptId': 'receipt-b', 'falseAttribution': True})

    def test_gate_allows_inconclusive_but_requires_one_correct_match_and_zero_false_attribution(self):
        rows = [
            {'id': 'positive', 'transform': 'png', 'outcome': 'matched', 'falseAttribution': False, 'quality': .91},
            {'id': 'damaged', 'transform': 'damaged', 'outcome': 'inconclusive', 'falseAttribution': False, 'quality': .62},
        ]
        summary = evaluate_decoder.summarize(rows, .90, {'coldLoadSeconds': 1.2, 'warmMeanSeconds': .2, 'peakRssBytes': 10})
        self.assertTrue(summary['eligibleForDisclosure'])
        self.assertEqual(summary['sampleSize'], 2)
        self.assertEqual(summary['transformCoverage'], ['damaged', 'png'])
        self.assertIn('frozen evaluated set', evaluate_decoder.markdown(summary))
        self.assertFalse(evaluate_decoder.summarize(rows[:1] + [dict(rows[1], falseAttribution=True)], .90, {})['eligibleForDisclosure'])
        self.assertFalse(evaluate_decoder.summarize(rows[1:], .90, {})['eligibleForDisclosure'])


class DecoderServerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.root.chmod(0o700)

    def exchange(self, request, result):
        job = self.root/request['job']
        job.mkdir(mode=0o700)
        (job/'input').write_bytes(b'private image')
        body = json.dumps(request).encode()
        source, output = io.BytesIO(struct.pack('>I', len(body)) + body), io.BytesIO()
        with patch.object(engine, 'load_encoder', return_value=object()), patch.object(engine, 'read_image', return_value=object()), patch.object(engine, 'decode_image', return_value=result):
            worker.serve_decoder(self.root, source, output)
        output.seek(0)
        length = struct.unpack('>I', output.read(4))[0]
        return json.loads(output.read(length))

    def test_framed_server_returns_only_exact_payload_or_inconclusive(self):
        payload = '01'*30 + '0'
        request = {'job': 'decode-one', 'evaluationIdentity': engine.DECODER_VERSION}
        self.assertEqual(self.exchange(request, engine.DecodeResult('decoded', payload, .95)),
                         {'status': 'decoded', 'evaluationIdentity': engine.DECODER_VERSION, 'payload': payload, 'quality': .95})
        request['job'] = 'decode-two'
        self.assertEqual(self.exchange(request, engine.DecodeResult('inconclusive', None, .7)),
                         {'status': 'inconclusive', 'evaluationIdentity': engine.DECODER_VERSION, 'quality': .7})

    def test_framed_server_rejects_escape_and_prints_no_payload(self):
        payload = '1'*61
        body = json.dumps({'job': '../escape', 'evaluationIdentity': engine.DECODER_VERSION}).encode()
        source, output, stderr = io.BytesIO(struct.pack('>I', len(body)) + body), io.BytesIO(), io.StringIO()
        with patch.object(engine, 'load_encoder', return_value=object()), contextlib.redirect_stderr(stderr):
            worker.serve_decoder(self.root, source, output)
        output.seek(4)
        self.assertEqual(json.loads(output.read())['status'], 'failed')
        self.assertNotIn(payload, stderr.getvalue())

    def test_framed_server_rejects_oversized_frame(self):
        with patch.object(engine, 'load_encoder', return_value=object()):
            with self.assertRaises(ValueError):
                worker.serve_decoder(self.root, io.BytesIO(struct.pack('>I', worker.FRAME_LIMIT + 1)), io.BytesIO())

if __name__ == '__main__':
    unittest.main()
