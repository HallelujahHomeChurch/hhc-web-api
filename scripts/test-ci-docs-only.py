from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('ci-docs-only.py')


class DocsOnlyTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.repo = Path(self.temp.name)
        self.git('init', '-q')
        self.git('config', 'user.email', 'ci-test@example.invalid')
        self.git('config', 'user.name', 'CI test')
        self.write('source.go', 'package example\n')
        self.commit()
        self.base = self.git('rev-parse', 'HEAD').strip()

    def tearDown(self):
        self.temp.cleanup()

    def git(self, *args):
        return subprocess.check_output(['git', *args], cwd=self.repo, text=True)

    def write(self, name, content='documentation\n'):
        path = self.repo / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)

    def commit(self):
        self.git('add', '.')
        self.git('commit', '-qm', 'fixture')

    def classify(self, event='pull_request', base=None, head=None):
        head = head or self.git('rev-parse', 'HEAD').strip()
        result = subprocess.run(['python3', str(SCRIPT), event, base or self.base, head],
                                cwd=self.repo, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout.strip()

    def test_markdown_and_document_images(self):
        self.write('docs/reviews/notes with spaces.md')
        self.write('docs/reviews/layout.svg')
        self.commit()
        self.assertEqual(self.classify(), 'docs_only=true')

    def test_readme_is_a_document(self):
        self.write('README.md')
        self.commit()
        self.assertEqual(self.classify(), 'docs_only=true')

    def test_merge_checkout_uses_verified_merge_tree(self):
        branch = self.git('branch', '--show-current').strip()
        self.git('checkout', '-qb', 'topic')
        self.write('docs/review.md')
        self.commit()
        self.git('checkout', '-q', branch)
        self.git('merge', '--no-ff', '-qm', 'merge fixture', 'topic')
        self.assertEqual(self.classify(), 'docs_only=true')

    def test_contracts_and_control_characters_fail_closed(self):
        for name in ['docs/contracts/review.md', 'docs/testdata/input.txt',
                     'docs/obs-capture/notes.md', 'docs/line\nfeed.md', 'docs/line\rfeed.md']:
            with self.subTest(name=name):
                previous = self.git('rev-parse', 'HEAD').strip()
                self.write(name)
                self.commit()
                self.assertEqual(self.classify(base=previous), 'docs_only=false')

    def test_mixed_source_always_runs_heavy_checks(self):
        self.write('docs/review.md')
        self.write('source.go', 'package changed\n')
        self.commit()
        self.assertEqual(self.classify(), 'docs_only=false')

    def test_runtime_and_security_inputs_are_not_documents(self):
        for name in ['docs/openapi.yaml', 'docs/fixtures.json', 'docs/obs-capture/testdata/input.md', '.github/workflows/ci.yml',
                     'Dockerfile', 'go.sum', 'scripts/ci-docs-only.py', 'AGENTS.md']:
            with self.subTest(name=name):
                previous = self.git('rev-parse', 'HEAD').strip()
                self.write(name)
                self.commit()
                self.assertEqual(self.classify(base=previous), 'docs_only=false')

    def test_rename_source_to_document_is_not_document_only(self):
        self.git('mv', 'source.go', 'README.md')
        self.commit()
        self.assertEqual(self.classify(), 'docs_only=false')

    def test_document_deletion_and_rename(self):
        self.write('docs/old.md')
        self.commit()
        self.base = self.git('rev-parse', 'HEAD').strip()
        self.git('mv', 'docs/old.md', 'docs/new.md')
        self.commit()
        self.assertEqual(self.classify(), 'docs_only=true')
        self.git('rm', 'docs/new.md')
        self.commit()
        self.assertEqual(self.classify(), 'docs_only=true')

    def test_symlink_with_document_name_is_not_a_document(self):
        self.repo.joinpath('docs').mkdir()
        self.repo.joinpath('docs/linked.md').symlink_to('../source.go')
        self.commit()
        self.assertEqual(self.classify(), 'docs_only=false')

    def test_executable_document_is_not_document_only(self):
        self.write('docs/executable.md')
        self.repo.joinpath('docs/executable.md').chmod(0o755)
        self.commit()
        self.assertEqual(self.classify(), 'docs_only=false')

    def test_unrelated_history_runs_full_checks(self):
        self.git('checkout', '-q', '--orphan', 'unrelated')
        self.write('docs/review.md')
        self.commit()
        self.assertEqual(self.classify(), 'docs_only=false')

    def test_invalid_revision_checkout_mismatch_empty_diff_and_manual_run(self):
        self.assertEqual(self.classify(), 'docs_only=false')
        self.write('docs/review.md')
        self.commit()
        self.assertEqual(self.classify(base='0' * 40), 'docs_only=false')
        self.assertEqual(self.classify(base='--help'), 'docs_only=false')
        self.assertEqual(self.classify(head=self.base), 'docs_only=false')
        for event in ['workflow_dispatch', 'push', 'pull_request_target', 'unknown']:
            self.assertEqual(self.classify(event=event), 'docs_only=false')


if __name__ == '__main__':
    unittest.main()
