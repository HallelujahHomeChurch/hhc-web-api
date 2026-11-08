import importlib.util
import json
import os
import tempfile
from unittest.mock import patch
import extractor_selection as selection
from pathlib import Path
import unittest

spec=importlib.util.spec_from_file_location('renderer',Path(__file__).with_name('select-ci-renderer.py'))
renderer=importlib.util.module_from_spec(spec);spec.loader.exec_module(renderer)
PIN={'commit':'a'*40,'image':'ghcr.io/hallelujahhomechurch/hhc-bulletin-renderer@sha256:'+'b'*64}

class RendererReuse(unittest.TestCase):
    def test_only_matching_authenticated_producer_reuses_fixed_digest(self):
        result=renderer.select(PIN,dict(PIN),'/runner')
        self.assertEqual(result,{'reuse':'true','commit':'a'*40,'image':PIN['image'],'context':'docker-image://'+PIN['image']})

    def test_missing_or_mismatched_evidence_builds_exact_source(self):
        for proof in (None,{'commit':'c'*40,'image':PIN['image']},{'commit':PIN['commit'],'image':'untrusted'}):
            result=renderer.select(PIN,proof,'/runner')
            self.assertEqual(result['reuse'],'false')
            self.assertEqual(result['commit'],'a'*40)
            self.assertEqual(result['image'],'')
            self.assertEqual(result['context'],'oci-layout:///runner/renderer-layout')

    def test_tags_other_producers_and_invalid_commits_fail_before_checkout(self):
        for pin in ({**PIN,'image':'ghcr.io/hallelujahhomechurch/hhc-bulletin-renderer:latest'},
                    {**PIN,'image':'ghcr.io/other/renderer@sha256:'+'b'*64},
                    {**PIN,'commit':'main'},{**PIN,'extra':True}):
            with self.assertRaises(ValueError):renderer.select(pin,None,'/runner')

    def test_reused_image_preparation_needs_no_renderer_checkout(self):
        previous=Path.cwd()
        def git(*args):
            return PIN['commit'] if args==('rev-parse','HEAD') else ''
        def docker(args):
            if args[:2]==['docker','run']:return ('c'*64).encode()
            if args[:3]==['docker','image','inspect']:
                return json.dumps([{'Os':'linux','Architecture':'amd64','Config':{'User':'65532:65532'}}]).encode()
            raise AssertionError('Unexpected external invocation')
        with tempfile.TemporaryDirectory() as directory:
            try:
                os.chdir(directory);target=Path('artifacts/extractor-selection');target.mkdir(parents=True)
                (target/'binaries.json').write_text(json.dumps({'parser.test':'d'*64,'extractor.test':'e'*64}))
                env={'GITHUB_SHA':PIN['commit'],'BASE_SHA':'f'*40,'GITHUB_OUTPUT':str(Path(directory)/'out'),
                     'CI_RENDERER_IMAGE':PIN['image'],'EXTRACTOR_IMAGE_REF':'fixture'}
                with patch.dict(os.environ,env),patch.object(selection,'git',side_effect=git),patch.object(selection,'changes',return_value=[('M','docs/readme.md',True)]),patch('extractor_evidence.producer_evidence',return_value=dict(PIN)),patch.object(selection.subprocess,'check_output',side_effect=docker),patch('image_config_digest.image_config_digest',return_value='sha256:'+'f'*64):
                    selection.prepare('ci')
                self.assertEqual(json.loads((target/'ci.json').read_text())['producer'],PIN)
                self.assertFalse(Path('.renderer-source').exists())
            finally:os.chdir(previous)

if __name__=='__main__':unittest.main()
