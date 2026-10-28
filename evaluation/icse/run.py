#!/usr/bin/env python3
"""Offline real-CLI checks; no native agent/model invocation or measured study claim."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile


def snapshot(root):
    result = {}
    if not root.exists():
        return result
    for path in sorted(root.rglob('*')):
        info = path.lstat()
        relative = str(path.relative_to(root))
        value = {'mode': stat.S_IMODE(info.st_mode)}
        if path.is_symlink():
            value['link'] = os.readlink(path)
        elif path.is_file():
            value['sha256'] = hashlib.sha256(path.read_bytes()).hexdigest()
        else:
            value['directory'] = True
        result[relative] = value
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--trace', action='store_true', help='On Linux, use strace to assert preflight starts no child process and makes no network calls')
    parser.add_argument('--output', type=Path, help='Write path-sanitized reports and the run summary to this directory')
    args = parser.parse_args()
    if args.trace and (sys.platform != 'linux' or not shutil.which('strace')):
        parser.error('--trace requires Linux and strace')
    binary = str(Path(args.binary).resolve())
    fixtures = Path(__file__).resolve().parent
    with tempfile.TemporaryDirectory(prefix='agentpack-icse-') as directory:
        root = Path(directory)
        home, definition = root / 'home', root / 'definition'
        native_home = root / 'native-home'
        native_home.mkdir()
        workspaces = [root / 'checkout-a', root / 'relocated' / 'checkout-b']
        for workspace in workspaces:
            workspace.mkdir(parents=True)
            (workspace / 'project.txt').write_text('benign evaluation workspace\n')
        env = dict(os.environ, HOME=str(native_home), USERPROFILE=str(native_home), XDG_CONFIG_HOME=str(native_home/'config'), AGENTPACK_HOME=str(home), AGENTPACK_STAGING_ROOT=str(root/'staging'), CLAUDE_CODE_PATH=binary, CURSOR_AGENT_PATH=binary)
        traced_preflights = 0
        samples = {}
        def run(argv, workspace, expected):
            nonlocal traced_preflights
            command = [binary, '--project-root', str(workspace), *argv]
            traced = args.trace and 'preflight' in argv
            trace = root / 'preflight.trace'
            if traced:
                command = ['strace', '-f', '-qq', '-s', '128', '-e', 'trace=process,network', '-o', str(trace), *command]
            proc = subprocess.run(command, cwd=workspace, env=env, capture_output=True, text=True, timeout=120 if traced else 30)
            if proc.returncode != expected:
                raise AssertionError(f'{argv}: expected exit {expected}, got {proc.returncode}: {proc.stderr}\n{proc.stdout}')
            if traced:
                calls = trace.read_text()
                assert len(re.findall(r'\bexecve(?:at)?\(', calls)) == 1, 'preflight attempted another executable'
                assert not re.search(r'\b(?:fork|vfork)\(', calls), 'preflight forked a process'
                for line in calls.splitlines():
                    if re.search(r'\bclone(?:3)?\(', line):
                        assert 'CLONE_THREAD' in line, 'preflight created a non-thread child'
                assert not re.search(r'\b(?:socket|connect|sendto|sendmsg|sendmmsg|bind|listen|accept|accept4)\(', calls), 'preflight attempted network I/O'
                traced_preflights += 1
            return proc
        run(['env', 'init', '--dir', str(definition), '--name', 'icse'], workspaces[0], 0)
        ids = []
        outcomes = []
        for workspace in workspaces:
            before = {str(p): snapshot(p) for p in (home, definition, workspace, native_home)}
            clean = run(['--env', str(definition), 'preflight', '--agent', 'claude', '--strict-external', '--contract', str(fixtures/'clean.json'), '--json'], workspace, 0)
            report = json.loads(clean.stdout)
            assert report['contract_results'][0]['result'] == 'satisfied', report
            ids.append(report['source_identity'])
            unknown = run(['--env', str(definition), 'preflight', '--agent', 'claude', '--strict-external', '--contract', str(fixtures/'unknown.json'), '--json'], workspace, 4)
            assert json.loads(unknown.stdout)['contract_results'][0]['result'] == 'unknown'
            violation = run(['--env', str(definition), 'preflight', '--agent', 'cursor', '--strict-external', '--contract', str(fixtures/'clean.json'), '--json'], workspace, 3)
            assert json.loads(violation.stdout)['overall_status'] == 'violated'
            after = {str(p): snapshot(p) for p in (home, definition, workspace, native_home)}
            assert before == after, 'pure preflight mutated files, modes, or links'
            if not samples:
                samples.update(clean=json.loads(clean.stdout), unknown=json.loads(unknown.stdout), violation=json.loads(violation.stdout))
            outcomes.append({'workspace':'relocated' if len(outcomes) else 'original','clean_exit':0,'unknown_exit':4,'unsupported_overlay_exit':3,'state_unchanged':True})
        assert ids[0] and ids[0] == ids[1], 'portable source ID changed on relocation'
        workspace = workspaces[0]
        lock_hash = hashlib.sha256((definition/'pack.lock').read_bytes()).hexdigest()
        faults = []
        settings = native_home/'.claude'/'settings.json'
        settings.parent.mkdir(parents=True)
        settings.write_text('{"env":{"ICSE_FIXTURE":"ambient"}}')
        before = {str(p): snapshot(p) for p in (home, definition, workspace, native_home)}
        inherited = run(['--env', str(definition), 'preflight', '--agent', 'claude', '--strict-external', '--policy', 'ci', '--json'], workspace, 3)
        report = json.loads(inherited.stdout)
        assert any(f['code'] == 'INHERITED_CONFIG' and f['severity'] == 'violation' for f in report['findings']), report
        assert before == {str(p): snapshot(p) for p in (home, definition, workspace, native_home)}
        settings.unlink()
        faults.append({'fault':'held_out_user_settings','exit':3,'detected':True})
        ambient = workspace/'.claude'/'skills'/'held-out-review'/'SKILL.md'
        ambient.parent.mkdir(parents=True)
        ambient.write_text('---\nname: held-out-review\ndescription: independently authored ambient fixture\n---\n\n# Ambient review\n')
        before = {str(p): snapshot(p) for p in (home, definition, workspace, native_home)}
        inherited = run(['--env', str(definition), 'preflight', '--agent', 'claude', '--strict-external', '--policy', 'ci', '--json'], workspace, 3)
        report = json.loads(inherited.stdout)
        assert any(f['code'] == 'AMBIENT_ARTIFACT' and f['source'] == 'held-out-review' and f['severity'] == 'violation' for f in report['findings']), report
        assert before == {str(p): snapshot(p) for p in (home, definition, workspace, native_home)}
        faults.append({'fault':'held_out_project_skill','exit':3,'detected':True})
        shutil.rmtree(workspace/'.claude')
        before = {str(p): snapshot(p) for p in (home, definition, workspace, native_home)}
        recovered = run(['--env', str(definition), 'preflight', '--agent', 'claude', '--strict-external', '--policy', 'ci', '--json'], workspace, 0)
        assert json.loads(recovered.stdout)['ok']
        assert before == {str(p): snapshot(p) for p in (home, definition, workspace, native_home)}
        faults.append({'fault':'remove_authored_ambient_inputs','exit':0,'recovered':True})
        skill = workspace/'.agents'/'skills'/'existing'/'SKILL.md'
        skill.parent.mkdir(parents=True)
        skill.write_text('---\nname: existing\ndescription: benign held-out fixture\n---\n\n# Existing\n')
        contract = root/'missing-named.json'
        contract.write_text(json.dumps({'schema_version':1,'unknown_required':'fail','requirements':[{'id':'missing-named','target':'claude','selector':{'category':'skill','name':'missing'},'predicate':'present','minimum_evidence':'generated'}]}))
        before = {str(p): snapshot(p) for p in (home, definition, workspace, native_home)}
        named = run(['--env', str(definition), 'preflight', '--agent', 'claude', '--contract', str(contract), '--json'], workspace, 3)
        report = json.loads(named.stdout)
        assert report['contract_results'][0]['result'] == 'violated', report
        assert before == {str(p): snapshot(p) for p in (home, definition, workspace, native_home)}
        faults.append({'fault':'different_skill_cannot_satisfy_named_requirement','exit':3,'detected':True})
        incomplete = root/'incomplete-receipt.json'
        incomplete.write_text(json.dumps({'schema_version':1,'coverage':[{'category':'skill','scope':'native_catalog','completeness':'complete'}]}))
        invalid = run(['--env', str(definition), 'preflight', '--agent', 'claude', '--contract', str(fixtures/'unknown.json'), '--receipt', str(incomplete), '--json'], workspace, 5)
        report = json.loads(invalid.stdout)
        assert not report['ok'] and all(r['result'] != 'satisfied' for r in report.get('contract_results',[])), report
        faults.append({'fault':'incomplete_receipt_claims_complete_coverage','exit':5,'detected':True})
        stale_receipt = root/'synthetic-stale-receipt.json'
        stale_receipt.write_text(json.dumps({
            'schema_version': 1, 'phase': 'probe', 'receipt_id': 'synthetic-stale-fixture',
            'source_id': ids[0], 'lock_digest': 'sha256:'+lock_hash,
            'workspace_digest': 'synthetic-old-workspace', 'generation_id': 'synthetic-old-generation',
            'mode': 'default', 'policy_digest': 'synthetic-old-policy',
            'target': {'adapter':'claude','adapter_revision':'synthetic','executable_identity':'synthetic','capability_revision':'synthetic','native_version':'not invoked'},
            'created_at':'2026-01-01T00:00:00Z', 'coverage':[], 'properties':[], 'findings':[],
            'effects':{'model_calls':False,'network':'not invoked; synthetic fixture','write_scope':'none'},
            'termination':{'status':'completed','native_exit_code':0}, 'overall_status':'unknown',
        }))
        stale = run(['--env', str(definition), 'preflight', '--agent', 'claude', '--contract', str(fixtures/'clean.json'), '--receipt', str(stale_receipt), '--json'], workspace, 4)
        samples['stale-receipt'] = json.loads(stale.stdout)
        assert any(f['code'] == 'RECEIPT_STALE' for f in samples['stale-receipt']['findings'])
        capsule = root/'support.json'
        run(['support', 'export', str(stale_receipt), '--output', str(capsule)], workspace, 0)
        samples['support'] = json.loads(capsule.read_text())
        assert str(root) not in capsule.read_text()
        faults.append({'fault':'synthetic_receipt_wrong_freshness_identities','exit':4,'detected':True})
        assert hashlib.sha256((definition/'pack.lock').read_bytes()).hexdigest() == lock_hash
        summary = {'suite':'icse-offline-contribution-fixtures-v4','agentpack_binary_sha256':hashlib.sha256(Path(binary).read_bytes()).hexdigest(),'platform':sys.platform,'native_agents_invoked':False,'preflight_syscall_checks':traced_preflights,'portable_source_id_equal':True,'cases':outcomes,'held_out_faults':faults,'lock_hash_control_unchanged':True}
        if args.output:
            args.output.mkdir(parents=True, exist_ok=True)
            for name, value in dict(samples, summary=summary).items():
                data = json.dumps(value, indent=2).replace(str(root), '<fixture>').replace(str(fixtures), '<fixtures>').replace(binary, '<agentpack>')
                (args.output/(name+'.json')).write_text(data+'\n')
        print(json.dumps(summary, indent=2))



if __name__ == '__main__':
    main()
