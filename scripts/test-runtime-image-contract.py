#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('runtime_images', Path(__file__).with_name('test-runtime-images.py'))
images = importlib.util.module_from_spec(spec)
spec.loader.exec_module(images)


class RuntimeIdentity(unittest.TestCase):
    def test_workloads_are_not_interchangeable(self):
        api = {'Labels': {'org.hhc.runtime': 'api'}, 'Entrypoint': ['/hhc-web-api'], 'User': 'nonroot:nonroot'}
        images.check_config(api, 'api', '/hhc-web-api')
        with self.assertRaises(AssertionError):
            images.check_config(api, 'watermark-worker', '/hhc-web-bulletin-worker')
        for key, value in [('User', 'root'), ('Entrypoint', ['/hhc-web-bulletin-worker']), ('Labels', {})]:
            with self.subTest(key=key), self.assertRaises(AssertionError):
                images.check_config({**api, key: value}, 'api', '/hhc-web-api')


if __name__ == '__main__':
    unittest.main()
