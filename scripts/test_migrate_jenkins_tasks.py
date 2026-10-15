import copy
import json
import unittest
from migrate_jenkins_tasks import build_plan, canonical_repo, frontend_codes


class JenkinsMigrationTests(unittest.TestCase):
    def test_repository_conversion_removes_credentials(self):
        self.assertEqual(canonical_repo('http://deploy:private-value@host/group/app.git'), 'git@host:group/app.git')
        self.assertEqual(canonical_repo('ssh://git@host/group/app.git'), 'git@host:group/app.git')
        with self.assertRaises(ValueError): canonical_repo('bad-repository')

    def test_frontend_mapping_is_parsed_without_running_legacy_code(self):
        source = """raise RuntimeError('never execute')
if app_name == 'web-bui':
    app_dist_dir = join_path(main_dist_dir, 'apps', '00')
"""
        self.assertEqual(frontend_codes(source), {'web-bui':'00'})

    def fixture(self):
        app = {'id':1,'app_name':'web-bingangl','app_type':'vue','git_repo':'git@host:group/web.git','default_branch':'master','vue_role':'sub','app_code':'20','node_port':0,'build_command':'','enabled':True}
        jobs = [
            {'name':'web-bingangl-butuo-yanshou','scripts':['cd /opt/deploy && ./build_vue_img.py web-bingangl feature-butuo-yanshou']},
            {'name':'web-bingangl-test','scripts':['cd /opt/deploy && ./build_vue_img.py web-bingangl main && ./publish_vue.py web-main']},
            {'name':'web-cdr-test','scripts':['cd /opt/deploy && ./build_vue_img.py web-cdr release && ./publish_vue.py web-cdr']},
            {'name':'qc-old-check','scripts':['python3 run_fixed_hash_check.py']},
        ]
        state = {'applications':[app], 'tasks':[], 'build_configs':[{'id':7,'category':'vue','status':'online','build_command':'npm run build:new','install_command':'yarn install'}]}
        legacy = {name:{'repo':'http://deploy:private-value@host/group/'+repo+'.git','resources':[{'name':'service','node_port':9001}]} for name,repo in [('web-bingangl','web'),('web-cdr','cdr')]}
        checks = [{'job':job['name'],'ok':True} for job in jobs]
        return {'inventory':{'jobs':jobs},'legacy_apps':legacy,'verification':checks,'state':state,'codes':{'web-bingangl':'20'},'namespace':'prod','runtime_ports':{'web-cdr':[{'nodePort':9002}]},'deployment_names':{'web-main','web-cdr'}}

    def test_multiple_branches_and_image_only_keep_distinct_tasks(self):
        plan = build_plan(**self.fixture())
        self.assertEqual(len(plan['tasks']),3)
        tasks = {task['job']:task for task in plan['tasks']}
        self.assertEqual(tasks['web-bingangl-test']['taskName'],'web-bingangl-prod')
        self.assertEqual(tasks['web-bingangl-butuo-yanshou']['mode'],'image_only')
        self.assertNotEqual(tasks['web-bingangl-butuo-yanshou']['taskName'],'web-bingangl-prod')
        cdr = next(row['values'] for row in plan['applications'] if row['values'].get('app_name')=='web-cdr')
        self.assertEqual(cdr['build_command'],'npm run build')
        self.assertEqual(cdr['node_port'],9002)
        self.assertNotIn('private-value',json.dumps(plan))
        self.assertEqual(len(plan['archives']),1)

    def test_missing_sources_and_mapping_are_disabled(self):
        fixture = self.fixture()
        fixture['legacy_apps'].pop('web-cdr')
        fixture['verification'] = []
        plan = build_plan(**fixture)
        self.assertTrue(all(not task['enabled'] for task in plan['tasks']))
        self.assertTrue(all(task['reasons'] for task in plan['tasks']))

    def test_replanning_preserves_existing_task_history(self):
        fixture = self.fixture()
        first = build_plan(**fixture)
        state = fixture['state']
        for change in first['applications']:
            if change['id']:
                next(row for row in state['applications'] if row['id']==change['id']).update(change['values'])
            else:
                state['applications'].append({'id':10,**copy.deepcopy(change['values'])})
        for index,task in enumerate(first['tasks']):
            app=next(row for row in state['applications'] if row['app_name']==task['app'])
            state['tasks'].append({'id':index+1,'task_name':task['taskName'],'application_id':app['id'],'git_branch':task['branch'],'deploy_mode':task['mode'],'enabled':task['enabled'],'build_config_id':task['buildConfigId'],'last_status':'SUCCESS'})
        second=build_plan(**fixture)
        self.assertTrue(all(task['existingId'] for task in second['tasks']))
        self.assertEqual(second['applications'],[])
        self.assertTrue(all(task['last_status']=='SUCCESS' for task in state['tasks']))


if __name__ == '__main__': unittest.main()
