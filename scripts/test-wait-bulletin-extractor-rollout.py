#!/usr/bin/env python3
import test_no_cloud
import importlib.util
from pathlib import Path

spec=importlib.util.spec_from_file_location('drain',Path(__file__).with_name('wait-bulletin-extractor-rollout.py'))
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
assert module.drained([], 'new')
assert module.drained([{'status':'Succeeded','images':['old']}], 'new')
assert module.drained([{'status':'Running','images':['new']}], 'new')
assert not module.drained([{'status':'Running','images':['old']}], 'new')
assert not module.drained([{'status':'Processing','images':None}], 'new')
try:
    module.drained([{}], 'new')
except ValueError:
    pass
else:
    raise AssertionError('Missing inventory accepted')
workflow=Path(__file__).parents[1].joinpath('.github/workflows/release.yml').read_text()
assert workflow.index('- name: Deploy isolated bulletin extractor') < workflow.index('- name: Deploy API')
assert workflow.index('python3 scripts/wait-bulletin-extractor-rollout.py') < workflow.index('- name: Deploy API')

producer=workflow.index('- name: Deploy API')
assert workflow.index('EXTRACTOR_RECOVERY_REQUIRED=false',workflow.index('python3 scripts/wait-bulletin-extractor-rollout.py')) < producer

assert "env.EXTRACTOR_COMPATIBLE_DEPLOYED != 'true'" in workflow
assert workflow.index('EXTRACTOR_COMPATIBLE_DEPLOYED=true') < producer
