#!/usr/bin/env python3
"""Import audited /opt/deploy Jenkins application jobs without running them.

Run on the platform host, using a protected inventory directory. Default: plan
only. --apply backs up PostgreSQL and commits only applications/task settings.
"""
import argparse
import ast
import collections
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
from urllib.parse import urlsplit

STANDARD_JOB = re.compile(r'\s*cd /opt/deploy && \./build_(app|vue)_img\.py\s+([\w-]+)\s+([\w./-]+)(?:\s*&&\s*\./publish_(?:app|vue)\.py\s+([\w-]+))?\s*')


def canonical_repo(url):
    if not url:
        return ''
    if '://' in url:
        parsed = urlsplit(url)
        if not parsed.hostname:
            raise ValueError('Repository host is missing')
        return 'git@' + parsed.hostname + ':' + parsed.path.lstrip('/')
    if re.fullmatch(r'git@[\w.-]+:[\w./-]+', url):
        return url
    raise ValueError('Unsupported repository URL; credentials will not be printed')


def frontend_codes(source):
    result = {}
    for node in ast.walk(ast.parse(source)):
        if not isinstance(node, ast.If) or not isinstance(node.test, ast.Compare):
            continue
        test = node.test
        if not (isinstance(test.left, ast.Name) and test.left.id == 'app_name' and len(test.comparators) == 1 and isinstance(test.ops[0], ast.Eq) and isinstance(test.comparators[0], ast.Constant)):
            continue
        for statement in node.body:
            if isinstance(statement, ast.Assign) and any(isinstance(target, ast.Name) and target.id == 'app_dist_dir' for target in statement.targets) and isinstance(statement.value, ast.Call):
                values = [arg.value for arg in statement.value.args if isinstance(arg, ast.Constant)]
                if len(values) >= 2 and values[-2] == 'apps':
                    result[test.comparators[0].value] = values[-1]
    return result


def default_config(app_type, configs, tasks):
    counts = collections.Counter(task['build_config_id'] for task in tasks)
    for config in sorted(configs, key=lambda row: (-counts[row['id']], row['id'])):
        command = (config['build_command'] + ' ' + (config.get('install_command') or '')).lower()
        tools = ['gradle'] if app_type == 'java' else ['npm', 'yarn', 'node', 'pnpm']
        if config['category'] == app_type and config['status'] == 'online' and any(tool in command for tool in tools):
            return config['id']
    raise ValueError('No active shared toolchain for ' + app_type)


def build_plan(inventory, legacy_apps, verification, state, codes, namespace, runtime_ports, deployment_names):
    apps = {row['app_name']: row for row in state['applications']}
    jobs, archives = [], []
    checks = {row['job']: row for row in verification}
    for job in inventory['jobs']:
        match = STANDARD_JOB.fullmatch('\n'.join(job['scripts']))
        if not match:
            archives.append({'job': job['name'], 'reason': '批量打包或固定提交号专项脚本，完整归档保留'})
            continue
        kind, name, branch, target = match.groups()
        jobs.append({'job': job['name'], 'app': name, 'appType': 'java' if kind == 'app' else 'vue', 'branch': branch, 'target': target, 'mode': 'build_image' if target else 'image_only', 'sourceEnabled': not job.get('disabled', False)})
    jobs.sort(key=lambda row: (not row['job'].endswith('-test'), row['job']))
    app_changes, tasks = {}, []
    reserved = {row['task_name'] for row in state['tasks']}
    seen_apps = set()
    for job in jobs:
        name, kind = job['app'], job['appType']
        existing = apps.get(name)
        legacy = legacy_apps.get(name)
        repo = canonical_repo(legacy['repo']) if legacy else ''
        reasons = []
        if not checks.get(job['job'], {}).get('ok'):
            reasons.append(checks.get(job['job'], {}).get('reason') or 'Git 仓库或分支未通过核验')
        if job['target'] and job['target'] not in deployment_names:
            reasons.append('原发布目标 Deployment 不存在')
        role = existing.get('vue_role') if existing else ('main' if name == 'web-main' else 'sub' if name in codes else 'standalone' if job['target'] == name else '')
        code = existing.get('app_code', '') if existing else str(codes.get(name, ''))
        if kind == 'vue' and not role:
            reasons.append('缺少前端部署映射，不能推断子应用路径')
        if not legacy:
            reasons.append('Jenkins 的应用配置缺失')
        if name == 'web-biaodanbjq':
            reasons.append('原分支没有 build:new，且原脚本未定义其 web-main 合并路径')
        if existing and not existing['enabled']:
            reasons.append('保留原应用停用状态')
        config_id = default_config(kind, state['build_configs'], state['tasks'])
        expected_command = 'npm run build' if name in ['web-cdr', 'web-opm'] else None
        changes = {}
        if existing:
            if existing['app_type'] != kind:
                raise ValueError('Existing application type conflicts: ' + name)
            if repo and canonical_repo(existing['git_repo']) != repo:
                changes['git_repo'] = repo
            if existing.get('default_branch') in ['', 'master'] and name not in seen_apps:
                changes['default_branch'] = job['branch']
            if expected_command and not existing.get('build_command'):
                changes['build_command'] = expected_command
            elif expected_command and existing.get('build_command') != expected_command:
                reasons.append('现有应用编译命令与 Jenkins 不同，保留现有配置待核对')
        else:
            changes = {'app_name': name, 'app_type': kind, 'git_repo': repo, 'default_branch': job['branch'], 'vue_role': role or '', 'app_code': code, 'is_gateway': name == 'his-gateway', 'artifact_name': name+'.jar' if kind == 'java' else 'web-main.zip' if role == 'main' else code+'.zip' if role == 'sub' else name+'.zip', 'node_port': 0, 'app_desc': 'Jenkins 迁移：'+job['job'], 'enabled': not reasons, 'build_command': expected_command or '', 'install_command': '', 'artifact_dir': ''}
        if job['target'] == name and (not existing or not existing.get('node_port')):
            actual = next((port['nodePort'] for port in runtime_ports.get(name, []) if port.get('nodePort')), None)
            configured = next((resource['node_port'] for resource in legacy.get('resources', []) if resource.get('node_port')), None) if legacy else None
            if actual or configured:
                changes['node_port'] = actual or configured
        if changes:
            current = app_changes.setdefault(name, {'id': existing['id'] if existing else None, 'before': existing, 'values': {}})
            for key, value in changes.items():
                if key in current['values'] and current['values'][key] != value:
                    raise ValueError('Conflicting application settings: ' + name + '/' + key)
                current['values'][key] = value
        seen_apps.add(name)
        enabled = job['sourceEnabled'] and not reasons
        mode = job['mode']
        base = name+'-'+namespace
        matching = [task for task in state['tasks'] if existing and task['application_id'] == existing['id'] and task['git_branch'] == job['branch'] and task['deploy_mode'] == mode]
        reuse = next((task for task in matching if task['task_name'] == base), matching[0] if matching else None)
        if reuse:
            task_name = reuse['task_name']
            # User-maintained tasks are reused without changing their settings/history.
            enabled = bool(reuse['enabled']) and enabled
        else:
            task_name = base
            if task_name in reserved:
                suffix = re.sub(r'[^A-Za-z0-9._-]', '-', job['branch'])
                task_name = (base+'-'+suffix)[:118]
                if task_name in reserved:
                    task_name += '-'+hashlib.sha256(job['job'].encode()).hexdigest()[:8]
            reserved.add(task_name)
        tasks.append({**job, 'taskName': task_name, 'existingId': reuse['id'] if reuse else None, 'buildConfigId': config_id, 'enabled': enabled, 'reasons': reasons, 'repo': repo})
    return {'namespace': namespace, 'applications': list(app_changes.values()), 'tasks': tasks, 'archives': archives}


def verify_import(plan, state):
    apps = {row['app_name']: row for row in state['applications']}
    tasks = {row['task_name']: row for row in state['tasks']}
    for change in plan['applications']:
        name = change['values'].get('app_name') or change['before']['app_name']
        if name not in apps or any(apps[name].get(key) != value for key, value in change['values'].items()):
            raise ValueError('Application write verification failed: '+name)
    for expected in plan['tasks']:
        actual = tasks.get(expected['taskName'])
        app = apps.get(expected['app'])
        if not actual or not app or actual['application_id'] != app['id'] or actual['git_branch'] != expected['branch'] or actual['deploy_mode'] != expected['mode'] or actual['enabled'] != expected['enabled']:
            raise ValueError('Task write verification failed: '+expected['job'])


def quote(value):
    if value is None:
        return 'NULL'
    if isinstance(value, bool):
        return 'true' if value else 'false'
    if isinstance(value, (int, float)):
        return str(value)
    return "'" + str(value).replace("'", "''") + "'"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--snapshot-dir', type=Path, required=True)
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--container', default='df-build-postgres')
    parser.add_argument('--database', default='df_build')
    parser.add_argument('--db-user', default='df_build')
    args = parser.parse_args()
    os.umask(0o077)
    folder = args.snapshot_dir.resolve()
    folder.mkdir(parents=True, exist_ok=True, mode=0o700)
    pg = ['docker', 'exec', '-i', args.container, 'psql', '-X', '-U', args.db_user, '-d', args.database, '-At', '-v', 'ON_ERROR_STOP=1']
    def read(sql):
        return subprocess.check_output(pg+['-c', sql], text=True).strip()
    state = {table: json.loads(read("SELECT COALESCE(json_agg(t),'[]') FROM (SELECT * FROM "+table+" ORDER BY id) t")) for table in ['applications','tasks','build_configs']}
    namespace = read("SELECT value FROM settings WHERE key='k8s_namespace'").strip() or 'default'
    if read("SELECT value FROM settings WHERE key='customer_env'").strip() != '本地':
        raise RuntimeError('Only import source tasks into a local environment')
    load = lambda name: json.loads((folder/name).read_text())
    inventory = load('inventory-private.json')
    legacy = load('jenkins-apps-private.json')['apps']
    deployment_names = {row['metadata']['name'] for row in load('deployments-before-private.json')['items']}
    codes = frontend_codes((folder/'sources/build_vue_img.py').read_text())
    plan = build_plan(inventory, legacy, load('git-verification.json'), state, codes, namespace, load('runtime-service-ports.json'), deployment_names)
    (folder/'migration-plan.json').write_text(json.dumps(plan, ensure_ascii=False, indent=2)+'\n')
    # Archive source configurations as evidence, never execute a Jenkins command.
    for job in inventory['jobs']:
        source = Path(job['config'])
        destination = folder/'sources/jobs'/job['name']/'config.xml'
        destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        if destination.exists() and destination.read_bytes() != source.read_bytes():
            raise RuntimeError('Jenkins configuration changed during migration: '+job['name'])
        if not destination.exists():
            shutil.copy2(source, destination)
    summary = {'sourceJobs': len(inventory['jobs']), 'nativeTasks': len(plan['tasks']), 'enabled': sum(row['enabled'] for row in plan['tasks']), 'disabled': [{'job': row['job'], 'reasons': row['reasons']} for row in plan['tasks'] if not row['enabled']], 'archived': [row['job'] for row in plan['archives']], 'newTasks': sum(row['existingId'] is None for row in plan['tasks']), 'newApplications': sum(row['id'] is None for row in plan['applications']), 'applied': False}
    if args.apply:
        for source in (folder/'sources').glob('*.py'):
            original = Path('/opt/deploy')/source.name
            if original.is_file() and original.read_bytes() != source.read_bytes():
                raise RuntimeError('Legacy helper changed after inventory: '+source.name)
        stamp = datetime.datetime.now().strftime('%Y%m%dT%H%M%S')
        (folder/('database-before-'+stamp+'.json')).write_text(json.dumps(state, ensure_ascii=False, indent=2))
        with (folder/('platform-before-'+stamp+'.dump')).open('wb') as dump:
            subprocess.run(['docker','exec',args.container,'pg_dump','-U',args.db_user,'-d',args.database,'-Fc'], stdout=dump, check=True)
        sql = ['BEGIN;', 'LOCK TABLE applications, tasks, pipelines IN SHARE ROW EXCLUSIVE MODE NOWAIT;', "DO $guard$ BEGIN IF EXISTS(SELECT 1 FROM pipelines WHERE status IN ('PENDING','RUNNING','DEPLOYING')) THEN RAISE EXCEPTION 'Active pipelines detected'; END IF; END $guard$;"]
        for change in plan['applications']:
            values = change['values']
            if change['id'] is not None:
                expected = quote(json.dumps(change['before'], ensure_ascii=False))+'::jsonb'
                sql.append("DO $guard$ BEGIN IF (SELECT to_jsonb(a) FROM applications a WHERE id="+str(change['id'])+") IS DISTINCT FROM "+expected+" THEN RAISE EXCEPTION 'Application changed after planning'; END IF; END $guard$;")
                sql.append('UPDATE applications SET '+', '.join(key+'='+quote(value) for key,value in values.items())+', updated_at=now() WHERE id='+str(change['id'])+';')
            else:
                sql.append('INSERT INTO applications ('+', '.join(values)+', created_at, updated_at) VALUES ('+', '.join(quote(value) for value in values.values())+', now(), now());')
        for task in plan['tasks']:
            if task['existingId'] is not None:
                continue
            values = {'task_name':task['taskName'], 'git_branch':task['branch'], 'build_config_id':task['buildConfigId'], 'deploy_mode':task['mode'], 'k8s_namespace':'', 'enabled':task['enabled']}
            sql.append('INSERT INTO tasks ('+', '.join(values)+', application_id, created_at, updated_at) SELECT '+', '.join(quote(value) for value in values.values())+', id, now(), now() FROM applications WHERE app_name='+quote(task['app'])+';')
        for task in plan['tasks']:
            check = "SELECT 1 FROM tasks t JOIN applications a ON a.id=t.application_id WHERE t.task_name="+quote(task['taskName'])+" AND a.app_name="+quote(task['app'])+" AND t.git_branch="+quote(task['branch'])+" AND t.deploy_mode="+quote(task['mode'])+" AND t.enabled="+quote(task['enabled'])
            sql.append("DO $guard$ BEGIN IF NOT EXISTS("+check+") THEN RAISE EXCEPTION 'Imported task verification failed'; END IF; END $guard$;")
        sql.append('COMMIT;')
        with (folder/'apply.log').open('w') as log:
            result = subprocess.run(pg+['-f','-'], input='\n'.join(sql), text=True, stdout=log, stderr=subprocess.STDOUT)
        if result.returncode:
            raise RuntimeError('Migration transaction rolled back; see protected apply.log')
        actual = {table: json.loads(read("SELECT COALESCE(json_agg(t),'[]') FROM (SELECT * FROM "+table+" ORDER BY id) t")) for table in ['applications','tasks']}
        verify_import(plan, actual)
        summary['applied'] = True
    (folder/'migration-summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2)+'\n')
    print(json.dumps(summary, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
