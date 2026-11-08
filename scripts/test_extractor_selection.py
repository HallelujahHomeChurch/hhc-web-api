import test_no_cloud
import copy
import json
import os
import unittest
from test_image_config_digest import ImageConfigTests
import tempfile
import subprocess
from pathlib import Path
from unittest.mock import patch
from extractor_selection import VERSIONS, GROUPS, SMOKE_GROUPS, affected, make_plan, validate, aggregate, digest, producer_versions
from extractor_evidence import producer_evidence, restore, evidence, REPO, PRODUCER, producer_image_config
import io
import zipfile
import hashlib

HEAD = 'a'*40
HASH = 'b'*64
CONFIG = b'{"os":"linux","architecture":"amd64"}'
IMAGE = 'sha256:' + hashlib.sha256(CONFIG).hexdigest()
DOCKER = '#!/usr/bin/env python3\nimport io, json, sys, tarfile\nconfig = b\'{"os":"linux","architecture":"amd64"}\'\nwith tarfile.open(sys.argv[sys.argv.index(\'--output\')+1], \'w\') as archive:\n    for name, data in [(\'manifest.json\', json.dumps([{\'RepoTags\':[\'fixture\'], \'Config\':\'config.json\', \'Layers\':[]}]).encode()), (\'config.json\', config)]:\n        member = tarfile.TarInfo(name); member.size = len(data); archive.addfile(member, io.BytesIO(data))\n'
VERIFY = {v: HASH for v in VERSIONS}
BINARIES = {v: HASH for v in ['parser.test', 'extractor.test']}


def plan(mode='ci', diff=None, checkpoint=None, producer=True):
    return make_plan(HEAD, 'c'*40, mode, diff or [('M', 'internal/content/service_test.go', True)], HASH, VERIFY,
                     {'commit': HEAD} if producer else None, checkpoint, image=IMAGE, binaries=BINARIES)


def receipts(p):
    return [dict(head=HEAD, run='123', attempt='1', mode=p['mode'], plan=p['digest'], image=IMAGE, group=i,
                 entries=[e for v in group for e in p['entries'] if e['version'] == v]) for i,group in enumerate(p['groups'])]


class SelectionTests(unittest.TestCase):
    def test_ci_skips_only_independent_api_entrypoints(self):
        from extractor_selection import ci_required
        for command in ('server', 'migrate', 'content-import', 'home-v2-migrate'):
            self.assertFalse(ci_required([('M', 'cmd/'+command+'/main.go', True)]))
        self.assertFalse(ci_required([('A','cmd/server/extra.go',True),('M','README.md',True)]))
        self.assertFalse(ci_required([('M','cmd/server/main.go',True),('M','docs/contracts/api.md',True)]))
        for diff in (None, [], [('D','cmd/server/main.go',True)], [('M','cmd/server/main.go',False)],
                     [('M','internal/content/service.go',True)], [('M','internal/recordings/service.go',True)],
                     [('M','internal/config/config.go',True)], [('M','infra/main.bicep',True)],
                     [('M','Dockerfile.extractor',True)], [('M','go.mod',True)],
                     [('M','cmd/extractor/main.go',True)], [('M','docs/tests/example.json',True)],
                     [('M','docs/snapshots/example.json',True)], [('M','docs/Contract.yaml',True)], [('M','cmd/server/main.go',True),('M','unknown',True)]):
            self.assertTrue(ci_required(diff),diff)

    def test_independent_api_source_reuses_only_matching_retained_full_evidence(self):
        proof={'full':{v:{'runtime':HASH,'verification':HASH} for v in VERSIONS}}
        diff=[('M','cmd/server/main.go',True)]
        self.assertTrue(all(e['tier']=='smoke' for e in plan('release',diff,proof)['entries']))
        self.assertTrue(all(e['tier']=='full' for e in plan('release',diff)['entries']))
        proof['full']['v1']['runtime']='e'*64
        self.assertEqual([e['version'] for e in plan('release',diff,proof)['entries'] if e['tier']=='full'],['v1'])

    def test_required_aggregate_accepts_only_explicit_extractor_skip(self):
        import re
        workflow=Path('.github/workflows/ci.yml').read_text()
        block=workflow.split('      - name: Require every verification job\n',1)[1].split('      - uses:',1)[0]
        script='\n'.join(line[10:] for line in block.split('        run: |\n',1)[1].splitlines())
        for flag,prepare,verify,want in [('false','skipped','skipped',0),('true','success','success',0),
                                         ('','skipped','skipped',1),('true','skipped','skipped',1),
                                         ('false','success','success',1)]:
            values={'changes.result':'success','changes.outputs.docs_only':'false','changes.outputs.extractor_required':flag,
                    'verify_policy.result':'success','verify_go.result':'success','verify_image.result':'success',
                    'prepare_extractor.result':prepare,'verify_extractor.result':verify}
            command=re.sub(r'\$\{\{ needs\.([^} ]+) \}\}',lambda m:values[m[1]],script)
            result=subprocess.run(['bash','-ec',command],capture_output=True)
            self.assertEqual(result.returncode==0,want==0,(flag,prepare,verify))


    def test_v11_requires_complete_plan_and_fresh_release_evidence(self):
        self.assertIn('v11', VERSIONS)
        self.assertEqual(sum(group.count('v11') for group in GROUPS), 1)
        self.assertEqual(sum(group.count('v11') for group in SMOKE_GROUPS), 1)
        prior = {'full': {v: {'runtime': HASH, 'verification': HASH} for v in VERSIONS if v != 'v11'}}
        p = plan('release', [('M', 'docs/unchanged.md', True)], prior)
        self.assertEqual([e['version'] for e in p['entries'] if e['tier'] == 'full'], ['v11'])
        p['entries'] = [e for e in p['entries'] if e['version'] != 'v11']
        p['digest'] = digest({k: v for k, v in p.items() if k != 'digest'})
        with self.assertRaises(AssertionError):
            validate(p, 'release', HEAD)

    def test_shared_unknown_unsafe_and_missing_diff_are_full(self):
        for diff in [None, [], [('M','go.mod',True)], [('M','internal/onlinebulletins/template_v10.go',True)],
                     [('M','unknown',True)], [('D','docs/readme.md',True)], [('M','docs/readme.md',False)],
                     [('R','docs/readme.md',True)], [('M','internal/content/service_test.go',True),('M','Dockerfile.extractor',True)]]:
            self.assertEqual(affected(diff), set(VERSIONS))

    def test_version_dependencies_and_unrelated_tests(self):
        self.assertEqual(affected([('M','internal/onlinebulletins/template_v9_test.go',True)]), {'v9','v10','v11'})
        self.assertEqual(affected([('M','internal/onlinebulletins/template_v10_test.go',True)]), {'v10','v11'})
        self.assertEqual(affected([('M','internal/content/service_test.go',True)]), set())
        self.assertEqual(plan()['groups'], SMOKE_GROUPS)
        self.assertEqual(plan(producer=False)['groups'], GROUPS)
        forced = make_plan(HEAD, 'c'*40, 'ci', [('M','internal/content/service_test.go',True)], HASH, VERIFY, {'commit': HEAD}, force=True, image=IMAGE, binaries=BINARIES)
        self.assertTrue(all(e['tier'] == 'full' for e in forced['entries']))

    def test_release_needs_matching_full_runtime_and_verification(self):
        proof = {'full': {v: {'runtime': HASH, 'verification': HASH} for v in VERSIONS}}
        self.assertTrue(all(e['tier']=='full' for e in plan('release')['entries']))
        self.assertTrue(all(e['tier']=='smoke' for e in plan('release',checkpoint=proof)['entries']))
        for field in ['runtime','verification']:
            broken=copy.deepcopy(proof);broken['full']['v9'][field]='d'*64
            selected=plan('release',checkpoint=broken)
            self.assertEqual([e['version'] for e in selected['entries'] if e['tier']=='full'], ['v9'])

    def test_closed_plan_and_exact_receipts(self):
        p=plan();validate(p,'ci',HEAD)
        with patch.dict(os.environ, GITHUB_RUN_ID='123', GITHUB_RUN_ATTEMPT='1'):
            aggregate(p,receipts(p))
            for key,value in [('head','d'*40),('attempt','2'),('mode','release'),('image','sha256:'+'d'*64),('plan','d'*64),('entries',[])]:
                r=receipts(p);r[0][key]=value
                with self.subTest(key=key),self.assertRaises(AssertionError):aggregate(p,r)
            for r in [[],receipts(p)*2]:
                with self.assertRaises(AssertionError):aggregate(p,r)
        for mutate in [lambda x:x.update(extra=True),lambda x:x['entries'][0].update(version='v11'),
                       lambda x:x.update(groups=[['v1; echo unsafe']]),lambda x:x['entries'][0].update(tier='skip'),
                       lambda x:x.update(binaries={'parser.test': HASH})]:
            p=plan();mutate(p);p['digest']=digest({k:v for k,v in p.items() if k!='digest'})
            with self.assertRaises(AssertionError):validate(p,'ci',HEAD)

    def test_retry_reuses_only_authenticated_successful_same_run_attempts(self):
        p=plan();r=receipts(p)
        jobs=[{'name':f'verify_extractor ({i})','conclusion':'success','head_sha':'e'*40} for i in range(len(p['groups']))]
        with patch.dict(os.environ,GITHUB_RUN_ID='123',GITHUB_RUN_ATTEMPT='2'),patch('extractor_evidence.api',side_effect=[{'head_sha':'e'*40},{'jobs':jobs}]):
            aggregate(p,r)
        for key,value in [('conclusion','failure'),('head_sha','f'*40),('name','other')]:
            bad=copy.deepcopy(jobs);bad[0][key]=value
            with patch.dict(os.environ,GITHUB_RUN_ID='123',GITHUB_RUN_ATTEMPT='2'),patch('extractor_evidence.api',side_effect=[{'head_sha':'e'*40},{'jobs':bad}]),self.assertRaises(AssertionError):aggregate(p,r)

    def test_shared_runner_rejects_skip_missing_cases_and_binary_mutation(self):
        script = Path(__file__).with_name('run_extractor_selection.py').resolve()
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            for d in ['bin','cmd/extractor','artifacts/extractor-selection']:(root/d).mkdir(parents=True)
            p=plan()
            for name in BINARIES:
                (root/'artifacts/extractor-selection'/name).write_bytes(b'fixture')
                p['binaries'][name]=digest(b'fixture')
            p['digest']=digest({k:v for k,v in p.items() if k!='digest'})
            (root/'plan.json').write_text(json.dumps(p))
            docker=root/'bin/docker';docker.write_text(DOCKER);docker.chmod(0o755)
            go=root/'bin/go'
            env={**os.environ,'PATH':str(root/'bin')+os.pathsep+os.environ['PATH'],'GITHUB_SHA':HEAD,'GITHUB_RUN_ID':'123','GITHUB_RUN_ATTEMPT':'1','EXTRACTOR_IMAGE_REF':'fixture'}
            names=['TestExtractorRuntimeSmoke']+['TestExtractorRuntimeSmoke/'+v for v in SMOKE_GROUPS[0]]
            for kind in ['pass','missing','skip']:
                events=[{'Action':'pass','Test':n} for n in (names[:-1] if kind=='missing' else names)]
                if kind=='skip':events.append({'Action':'skip','Test':names[-1]})
                go.write_text("#!/bin/sh\ncat <<'EVENTS'\n"+'\n'.join(json.dumps(e) for e in events)+'\nEVENTS\n');go.chmod(0o755)
                result=subprocess.run(['python3',str(script),'ci','plan.json','0','results-'+kind],cwd=root,env=env,capture_output=True)
                self.assertEqual(result.returncode==0,kind=='pass',result.stderr)
                self.assertEqual((root/('results-'+kind)/'0.json').exists(),kind=='pass')
            (root/'artifacts/extractor-selection/parser.test').write_bytes(b'changed')
            result=subprocess.run(['python3',str(script),'ci','plan.json','0','results-corrupt'],cwd=root,env=env,capture_output=True)
            self.assertNotEqual(result.returncode,0)
            self.assertFalse((root/'results-corrupt').exists())

    def test_same_process_union_preserves_both_tiers_and_candidate_identity(self):
        script = Path(__file__).with_name('run_extractor_selection.py').resolve()
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for directory in ['bin', 'cmd/extractor', 'artifacts/extractor-selection']:
                (root / directory).mkdir(parents=True)
            ci = plan('ci', [('M', 'internal/onlinebulletins/template_v10_test.go', True)])
            release = plan('release', [('M', 'go.mod', True)])
            for candidate in [ci, release]:
                for name in BINARIES:
                    (root / 'artifacts/extractor-selection' / name).write_bytes(b'fixture')
                    candidate['binaries'][name] = digest(b'fixture')
                candidate['digest'] = digest({k: v for k, v in candidate.items() if k != 'digest'})
            docker = root / 'bin/docker'
            docker.write_text(DOCKER)
            docker.chmod(0o755)
            go = root / 'bin/go'
            go.write_text('''#!/usr/bin/env python3
import json, os, re, sys
pattern = sys.argv[-1]
test, versions = re.fullmatch(r'\^(\w+)\$/\^\(([^)]+)\)\$', pattern).groups()
names = [test] + [test + '/' + v for v in versions.split('|')]
with open(os.environ['CALLS'], 'a') as calls:
    calls.write(json.dumps(names) + '\\n')
for name in names:
    action = 'skip' if os.environ.get('FAIL_CASE') == name else 'pass'
    print(json.dumps({'Action': action, 'Test': name}))
''')
            go.chmod(0o755)
            env = {**os.environ, 'PATH': str(root / 'bin') + os.pathsep + os.environ['PATH'],
                   'GITHUB_SHA': HEAD, 'GITHUB_RUN_ID': '123', 'GITHUB_RUN_ATTEMPT': '1',
                   'EXTRACTOR_IMAGE_REF': 'fixture', 'CALLS': str(root / 'calls')}
            (root / 'ci.json').write_text(json.dumps(ci))
            command = ['python3', str(script), 'ci', 'ci.json', '0', 'ci-results', 'release.json', 'release-results']
            def run(candidate, failure=''):
                (root / 'release.json').write_text(json.dumps(candidate))
                return subprocess.run(command, cwd=root, env={**env, 'FAIL_CASE': failure}, capture_output=True)
            result = run(release)
            self.assertEqual(result.returncode, 0, result.stderr)
            calls = [json.loads(line) for line in (root / 'calls').read_text().splitlines()]
            full = 'TestExtractorIsolationLoadGate'
            smoke = 'TestExtractorRuntimeSmoke'
            self.assertEqual(calls, [[full, full+'/v11', full+'/v4', full+'/v5'],
                                     [smoke, smoke+'/v4', smoke+'/v5']])
            for candidate, directory in [(ci, 'ci-results'), (release, 'release-results')]:
                self.assertEqual(json.loads((root / directory / '0.json').read_text()), receipts(candidate)[0])
            # A skipped case invalidates both outputs, including prior successful receipts.
            self.assertNotEqual(run(release, full+'/v4').returncode, 0)
            for directory in ['ci-results', 'release-results']:
                self.assertFalse((root / directory / '0.json').exists())
            for field in ['head', 'image', 'runtime', 'verification', 'binaries']:
                bad = copy.deepcopy(release)
                if field in ['verification', 'binaries']:
                    bad[field][next(iter(bad[field]))] = 'd'*64
                else:
                    bad[field] = 'sha256:'+'d'*64 if field == 'image' else 'd'*(40 if field == 'head' else 64)
                bad['digest'] = digest({k: v for k, v in bad.items() if k != 'digest'})
                self.assertNotEqual(run(bad).returncode, 0, field)
                self.assertFalse((root / 'release-results/0.json').exists())

    def test_sealer_preserves_original_full_evidence_and_rejects_changed_inputs(self):
        script = Path(__file__).with_name('seal_extractor_evidence.py').resolve()
        prior = {v: dict(runtime=HASH, verification=HASH, head='c'*40, runId='100', runAttempt='1') for v in VERSIONS}
        p = plan('release', [('M', 'internal/onlinebulletins/template_v10_test.go', True)], {'full': prior})
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); selection = root/'artifacts/extractor-selection'
            selection.mkdir(parents=True); (root/'extractor-results').mkdir()
            (selection/'release.json').write_text(json.dumps(p))
            (selection/'baseline.json').write_text(json.dumps({'full': prior}))
            for r in receipts(p): (root/'extractor-results'/f"{r['group']}.json").write_text(json.dumps(r))
            env = {**os.environ, 'GITHUB_SHA': HEAD, 'GITHUB_RUN_ID': '123', 'GITHUB_RUN_ATTEMPT': '1', 'ACCEPTANCE_ATTEMPT': '1',
                   'DEPLOYED_COMMIT': HEAD, 'DEPLOYED_API_DIGEST': IMAGE,
                   'DEPLOYED_EXTRACTOR_DIGEST': IMAGE}
            output = root/'artifacts/extractor-release-evidence/checkpoint.json'
            result = subprocess.run(['python3', str(script)], cwd=root, env=env, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            sealed = json.loads(output.read_text()); output.unlink()
            self.assertEqual(sealed['full']['v9'], prior['v9'])
            self.assertEqual(sealed['full']['v10']['runId'], '123')
            for broken in ['baseline', 'deployment', 'acceptance']:
                bad_env = dict(env)
                if broken == 'baseline':
                    prior['v9']['runtime'] = 'd'*64
                    (selection/'baseline.json').write_text(json.dumps({'full': prior}))
                else:
                    prior['v9']['runtime'] = HASH
                    (selection/'baseline.json').write_text(json.dumps({'full': prior}))
                    if broken == 'deployment':bad_env['DEPLOYED_COMMIT'] = 'd'*40
                    else:bad_env['ACCEPTANCE_ATTEMPT'] = '0'
                result = subprocess.run(['python3', str(script)], cwd=root, env=bad_env, capture_output=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(output.exists())

    def test_producer_config_only_changes_are_not_filesystem_equivalent(self):
        image = 'ghcr.io/hallelujahhomechurch/hhc-bulletin-renderer@' + IMAGE
        config = {'os': 'linux', 'architecture': 'amd64', 'config': {'Env': ['MODE=old'], 'User': '65532:65532', 'Entrypoint': ['node']}}
        with patch('extractor_evidence.subprocess.check_output', return_value=json.dumps(config).encode()):
            before = producer_image_config(image)
        for field, value in [('Env', ['MODE=new']), ('User', '0:0'), ('Entrypoint', ['other'])]:
            changed = copy.deepcopy(config); changed['config'][field] = value
            with patch('extractor_evidence.subprocess.check_output', return_value=json.dumps(changed).encode()):
                after = producer_image_config(image)
                self.assertNotEqual(after, before)
                prior = dict(runtime=HASH, config=before, versions=VERIFY)
                self.assertEqual(producer_versions(prior, {**prior, 'config': after}), set(VERSIONS))
                self.assertEqual(producer_versions(prior, prior), set())
        with patch('extractor_evidence.subprocess.check_output', return_value=b'{"linux/amd64":{}}'), self.assertRaises(KeyError):
            producer_image_config(image)

    @patch('extractor_evidence.Path.read_text', side_effect=FileNotFoundError)
    def test_old_or_missing_producer_requires_full(self, _):
        self.assertIsNone(producer_evidence())

    @patch('extractor_evidence.runs', return_value=[])
    def test_first_release_requires_full(self, _):
        with patch.dict(os.environ,GITHUB_RUN_ID='123'):self.assertIsNone(restore())

    def test_expired_failed_and_non_forward_release_require_full(self):
        with patch.dict(os.environ,GITHUB_RUN_ID='123'),patch('extractor_evidence.runs',return_value=[{'id':12,'updated_at':'2026-10-25'}]),patch('extractor_evidence.evidence',side_effect=AssertionError('expired/failed')):
            self.assertIsNone(restore())
        proof={'head':'c'*40,'full':{v:{'runtime':HASH,'verification':HASH,'runId':'12','runAttempt':'1'} for v in VERSIONS}}
        with patch.dict(os.environ,GITHUB_RUN_ID='123'),patch('extractor_evidence.runs',return_value=[{'id':12,'updated_at':'2026-10-25'}]),patch('extractor_evidence.evidence',return_value=proof),patch('extractor_evidence.api',return_value={}),patch('extractor_evidence.subprocess.run',side_effect=OSError('non-ancestor')):
            self.assertIsNone(restore())

    def test_release_archive_provenance_digest_expiration_and_attempt(self):
        run=dict(status='completed',conclusion='success',path='.github/workflows/release.yml',head_repository={'full_name':REPO},event='push',head_branch='main',head_sha=HEAD,id=12,run_attempt=1)
        value=dict(schema=3,head=HEAD,runId='12',runAttempt='1',apiDigest='sha256:'+'e'*64,extractorDigest='sha256:'+'f'*64)
        buf=io.BytesIO()
        with zipfile.ZipFile(buf,'w') as z:z.writestr('checkpoint.json',json.dumps(value))
        data=buf.getvalue()
        artifact=dict(name='extractor-release-evidence-12-1',expired=False,size_in_bytes=len(data),id=9,digest='sha256:'+hashlib.sha256(data).hexdigest())
        with patch('extractor_evidence.api',side_effect=[{'artifacts':[artifact]},data]):
            self.assertEqual(evidence(REPO,run,'extractor-release-evidence'),value)
        for old_schema in (1,2):
            legacy={**value,'schema':old_schema};old_buffer=io.BytesIO()
            with zipfile.ZipFile(old_buffer,'w') as archive:archive.writestr('checkpoint.json',json.dumps(legacy))
            old_data=old_buffer.getvalue();old_artifact={**artifact,'size_in_bytes':len(old_data),'digest':'sha256:'+hashlib.sha256(old_data).hexdigest()}
            with patch('extractor_evidence.api',side_effect=[{'artifacts':[old_artifact]},old_data]),self.assertRaises(AssertionError):
                evidence(REPO,run,'extractor-release-evidence')
        producer_value=dict(schema=1,head=HEAD,runId='12',runAttempt='1');producer_buffer=io.BytesIO()
        with zipfile.ZipFile(producer_buffer,'w') as archive:archive.writestr('checkpoint.json',json.dumps(producer_value))
        producer_data=producer_buffer.getvalue();producer_artifact={**artifact,'name':'renderer-release-evidence-12-1','size_in_bytes':len(producer_data),'digest':'sha256:'+hashlib.sha256(producer_data).hexdigest()}
        producer_run={**run,'head_repository':{'full_name':PRODUCER}}
        with patch('extractor_evidence.api',side_effect=[{'artifacts':[producer_artifact]},producer_data]):
            self.assertEqual(evidence(PRODUCER,producer_run,'renderer-release-evidence'),producer_value)
        for field,new in [('expired',True),('digest','sha256:'+'0'*64),('name','extractor-release-evidence-12-2')]:
            bad={**artifact,field:new}
            with patch('extractor_evidence.api',side_effect=[{'artifacts':[bad]},data]),self.assertRaises(AssertionError):evidence(REPO,run,'extractor-release-evidence')
        for field,new in [('conclusion','failure'),('event','pull_request'),('head_branch','topic'),('path','.github/workflows/ci.yml')]:
            with patch('extractor_evidence.api', return_value={'jobs': [], 'total_count': 0}), self.assertRaises(AssertionError):evidence(REPO,{**run,field:new},'extractor-release-evidence')

    def test_smoke_only_origin_cannot_become_full(self):
        proof={'head':'c'*40,'full':{v:{'runtime':HASH,'verification':HASH,'runId':'12','runAttempt':'1'} for v in VERSIONS}}
        original=copy.deepcopy(proof);original['full']['v1']['runId']='11'
        with patch.dict(os.environ,GITHUB_RUN_ID='123'),patch('extractor_evidence.runs',return_value=[{'id':13,'updated_at':'2026-10-25'}]),patch('extractor_evidence.evidence',side_effect=[proof,original]),patch('extractor_evidence.api',return_value={}):
            self.assertIsNone(restore())


class CheckpointTests(unittest.TestCase):
    def setUp(self):
        self.run = dict(status='completed', conclusion='failure',
                        path='.github/workflows/release.yml',
                        head_repository={'full_name': REPO}, event='push',
                        head_branch='main', head_sha=HEAD, id=12, run_attempt=1)
        self.value = dict(schema=3, head=HEAD, runId='12', runAttempt='1', apiDigest='sha256:'+'e'*64, extractorDigest='sha256:'+'f'*64)
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, 'w') as archive:
            archive.writestr('checkpoint.json', json.dumps(self.value))
        self.data = buffer.getvalue()
        self.artifact = dict(name='extractor-release-evidence-12-1', expired=False,
                             size_in_bytes=len(self.data), id=9,
                             digest='sha256:' + hashlib.sha256(self.data).hexdigest())
        self.jobs = [dict(name=name, head_sha=HEAD, status='completed', conclusion=result)
                     for name, result in [('public_acceptance', 'success'),
                                          ('seal_extractor_evidence', 'success'),
                                          ('verify_extractor (0)', 'success'),
                                          ('publish_openapi', 'failure')]]

    def read(self, run=None, jobs=None, total=None):
        jobs = self.jobs if jobs is None else jobs
        def api(repo, path, raw=False):
            if path == 'actions/runs/12/attempts/1':
                return self.run
            if path == 'actions/runs/12/attempts/1/jobs?per_page=100':
                return dict(jobs=jobs, total_count=len(jobs) if total is None else total)
            if path == 'actions/runs/12/artifacts?per_page=100':
                return dict(artifacts=[self.artifact])
            if path == 'actions/artifacts/9/zip' and raw:
                return self.data
            self.fail('Unexpected API: ' + path)
        with patch('extractor_evidence.api', side_effect=api):
            return evidence(REPO, run or self.run, 'extractor-release-evidence')

    def test_openapi_failure_keeps_verified_checkpoint(self):
        self.assertEqual(self.read(), self.value)

    def test_publication_only_retry_retains_the_earlier_sealed_attempt(self):
        self.assertEqual(self.read(run={**self.run, 'run_attempt': 2, 'conclusion': 'success'}), self.value)

    def test_other_failures_missing_jobs_partial_pages_and_stale_head_reject(self):
        for name in ['public_acceptance', 'seal_extractor_evidence', 'verify_extractor (0)']:
            for result in ['failure', 'cancelled', 'skipped']:
                if name.startswith('verify_extractor') and result == 'skipped':
                    continue
                jobs = [{**job, 'conclusion': result} if job['name'] == name else job for job in self.jobs]
                with self.subTest(name=name, result=result), self.assertRaises(AssertionError):
                    self.read(jobs=jobs)
        for jobs in [self.jobs[:-1], self.jobs[1:], self.jobs + [self.jobs[0]],
                     [{**job, 'head_sha': 'b' * 40} for job in self.jobs]]:
            with self.assertRaises(AssertionError):
                self.read(jobs=jobs)
        with self.assertRaises(AssertionError):
            self.read(total=101)

    def test_untrusted_or_unfinished_runs_never_gain_exception(self):
        for field, value in [('event', 'pull_request'), ('head_branch', 'topic'),
                             ('head_repository', {'full_name': 'outside/repo'}),
                             ('status', 'in_progress'), ('conclusion', 'cancelled')]:
            with self.subTest(field=field), self.assertRaises(AssertionError):
                self.read(run={**self.run, field: value})
        producer = {**self.run, 'head_repository': {'full_name': PRODUCER}}
        with self.assertRaises(AssertionError):
            evidence(PRODUCER, producer, 'renderer-release-evidence')

    def test_rerun_preserves_images_from_an_accepted_publication_failure(self):
        from extractor_evidence import guard
        api_image = 'example.azurecr.io/example/hhc-web-api@sha256:' + 'c' * 64
        extractor_image = 'example.azurecr.io/example/hhc-web-bulletin-extractor@sha256:' + 'd' * 64
        proof = dict(apiDigest=api_image.split('@')[1], extractorDigest=extractor_image.split('@')[1])
        env = dict(GITHUB_RUN_ID='13', GITHUB_SHA=HEAD, IMAGE_REF=api_image,
                   EXTRACTOR_IMAGE_REF=extractor_image)
        with patch.dict(os.environ, env), patch('extractor_evidence.runs', return_value=[self.run]), \
                patch('extractor_evidence.subprocess.run'), \
                patch('extractor_evidence.sealed_publication_failure', return_value=True), \
                patch('extractor_evidence.evidence', return_value=proof):
            guard()
            with patch.dict(os.environ, IMAGE_REF='changed'), self.assertRaises(AssertionError):
                guard()
            with patch('extractor_evidence.sealed_publication_failure', side_effect=OSError('API unavailable')), self.assertRaises(OSError):
                guard()
            with patch('extractor_evidence.evidence', side_effect=AssertionError('expired evidence')), self.assertRaises(AssertionError):
                guard()

    def test_same_run_later_attempt_pins_previously_accepted_images(self):
        from extractor_evidence import guard
        proof = dict(apiDigest='sha256:'+'c'*64, extractorDigest='sha256:'+'d'*64)
        env = dict(GITHUB_RUN_ID='12', GITHUB_RUN_ATTEMPT='2', GITHUB_SHA=HEAD,
                   IMAGE_REF='example.azurecr.io/example/hhc-web-api@'+proof['apiDigest'],
                   EXTRACTOR_IMAGE_REF='example.azurecr.io/example/hhc-web-bulletin-extractor@'+proof['extractorDigest'])
        with patch.dict(os.environ, env), patch('extractor_evidence.runs', return_value=[]), \
                patch('extractor_evidence.api', return_value=self.run) as api, \
                patch('extractor_evidence.subprocess.run'), \
                patch('extractor_evidence.sealed_publication_failure', return_value=True), \
                patch('extractor_evidence.evidence', return_value=proof):
            guard()
            api.assert_any_call(REPO, 'actions/runs/12/attempts/1')
            with patch.dict(os.environ, EXTRACTOR_IMAGE_REF='changed'), self.assertRaises(AssertionError):
                guard()

    def test_unaccepted_non_ancestor_failure_keeps_old_skip_behavior(self):
        from extractor_evidence import guard
        with patch.dict(os.environ, GITHUB_RUN_ID='13', GITHUB_RUN_ATTEMPT='1'), \
                patch('extractor_evidence.runs', return_value=[self.run]), \
                patch('extractor_evidence.sealed_publication_failure', return_value=False), \
                patch('extractor_evidence.subprocess.run', return_value=subprocess.CompletedProcess([], 1)):
            guard()

    def test_historical_failed_ancestor_does_not_query_actions_jobs(self):
        from extractor_evidence import guard
        with patch.dict(os.environ, GITHUB_RUN_ID='13', GITHUB_RUN_ATTEMPT='1', GITHUB_SHA='b' * 40), \
                patch('extractor_evidence.runs', return_value=[self.run]), \
                patch('extractor_evidence.subprocess.run', return_value=subprocess.CompletedProcess([], 0)), \
                patch('extractor_evidence.sealed_publication_failure', side_effect=AssertionError('unrelated API query')):
            guard()

    def test_publication_failure_still_checks_artifact_identity(self):
        self.artifact['digest'] = 'sha256:' + '0' * 64
        with self.assertRaises(AssertionError):
            self.read()

    def test_dag_seals_after_acceptance_even_when_publication_fails(self):
        workflow = Path('.github/workflows/release.yml').read_text()
        seal = workflow.split('\n  seal_extractor_evidence:\n', 1)[1]
        header = seal.split('    runs-on:', 1)[0]
        self.assertNotIn('publish_openapi', header)
        self.assertIn("needs.public_acceptance.result == 'success'", header)
        self.assertIn('!cancelled()', header)
        publication = workflow.split('\n  publish_openapi:\n', 1)[1].split('\n  seal_extractor_evidence:', 1)[0]
        self.assertNotIn('continue-on-error:', publication)
        self.assertIn('retention-days: 90', seal)


if __name__ == '__main__':unittest.main()
