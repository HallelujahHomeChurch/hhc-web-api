import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch
from image_config_digest import archive_config_digest, image_config_digest


class ImageConfigTests(unittest.TestCase):
    def test_config_identity_and_invalid_archives(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'image.tar'
            def write(arch='amd64', tag='fixture', extra=None):
                config = json.dumps({'os': 'linux', 'architecture': arch, 'rootfs': {'type': 'layers', 'diff_ids': []}}).encode()
                entries = [('manifest.json', json.dumps([{'RepoTags': [tag], 'Config': 'config.json', 'Layers': []}]).encode()), ('config.json', config)]
                if extra: entries.append(extra)
                with tarfile.open(path, 'w') as archive:
                    for name, value in entries:
                        member = tarfile.TarInfo(name); member.size = len(value)
                        archive.addfile(member, io.BytesIO(value))
                return 'sha256:' + hashlib.sha256(config).hexdigest()
            expected = write()
            self.assertEqual(archive_config_digest(path, 'fixture'), expected)
            def export(args, **kwargs):
                Path(args[args.index('--output') + 1]).write_bytes(path.read_bytes())
            with patch('image_config_digest.subprocess.run', side_effect=export):
                self.assertEqual(image_config_digest('fixture'), expected)
            for kwargs in [dict(arch='arm64'), dict(tag='other'), dict(extra=('config.json', b'{}')), dict(extra=('../outside', b'x'))]:
                write(**kwargs)
                with self.assertRaises(ValueError): archive_config_digest(path, 'fixture')


if __name__ == '__main__':
    unittest.main()
