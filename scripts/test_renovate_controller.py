"""API-level reconciliation tests: the fake models GitHub, not production code."""
import base64
import copy
import json
import unittest
from renovate_policy import GitHub, UPDATES, reconcile, MARKER

class Fake(GitHub):
    def __init__(self):
        super().__init__('', 'fin-assist/x')
        self.target='main'; self.actions=[]; self.comments=[]
        self.policy={'baseBranchPatterns':['main']}
        self.pr={'number':1,'state':'open','commits':1,'user':{'id':29139614,'login':'renovate[bot]'},
                 'head':{'sha':'abc','ref':'renovate/x','repo':{'full_name':self.repo}},
                 'base':{'ref':'main','sha':'base'},'mergeable':True,'mergeable_state':'clean',
                 'body':UPDATES+base64.b64encode(json.dumps([{'depName':'x','newValue':'v2.0.0','datasource':'npm'}]).encode()).decode()+' -->'}
        self.commits=[{'author':{'id':29139614},'_trusted_signer':True,'commit':{'verification':{'verified':True}}}]
        self.run={'id':1,'run_attempt':1,'head_sha':'abc','status':'completed','conclusion':'success'}
        self.jobs=[{'name':'go','status':'completed','conclusion':'success','html_url':'https://github.com/job'}]
        self.checks=[dict(self.jobs[0],app={'id':15368})]
        self.config={'requiredChecks':['go'],'controllerLogin':'controller'}
        self.protected=True; self.before_fresh=None
    def base(self): return self.target
    def read_policy(self):return copy.deepcopy(self.policy),'policy-sha'
    def write_policy(self,policy,sha):self.policy=copy.deepcopy(policy);self.actions.append(('policy',copy.deepcopy(policy)))
    def signed_commits(self,number):return self.commits
    def current_run(self,pr):return copy.deepcopy(self.run)
    def protection(self,base,names):return self.protected
    def native_checks(self,base,checks,statuses,jobs):return jobs if all(c['status']=='completed' and c['conclusion'] in ('success','failure') for c in jobs) else None
    def pages(self,path,key=None):
        if path=='pulls?state=open':return [self.pr] if self.pr['state']=='open' else []
        if path.endswith('/comments'):return self.comments
        if '/jobs' in path:return copy.deepcopy(self.jobs)
        if path.endswith('/check-runs'):return copy.deepcopy(self.checks)
        raise AssertionError(path)
    def get(self,path):
        if path=='pulls/1':return copy.deepcopy(self.pr)
        if path.endswith('/status'):return {'total_count':0,'state':'pending','statuses':[]}
        raise AssertionError(path)
    def fresh(self,*args,**kwargs):
        if self.before_fresh:self.before_fresh(self)
        return super().fresh(*args,**kwargs)
    def request(self,method,path,data=None):
        self.actions.append((path,data))
        if path=='issues/1/comments':self.comments.append({'id':10,'user':{'login':'controller'},'body':data['body']})
        if path=='issues/comments/10':self.comments[0]['body']=data['body']
        if path=='pulls/1':self.pr['state']=data['state']
        return {'id':10}

class ControllerTests(unittest.TestCase):
    def test_green_merges_expected_sha(self):
        api=Fake();reconcile(api,api.config)
        self.assertIn(('pulls/1/merge',{'sha':'abc','merge_method':'squash'}),api.actions)
    def test_no_merge_after_new_head_or_release(self):
        for update in (lambda a:a.pr['head'].update(sha='new'),lambda a:setattr(a,'target','release/0.3.0')):
            api=Fake();api.before_fresh=update;reconcile(api,api.config)
            self.assertFalse(any(path=='pulls/1/merge' for path,_ in api.actions))
    def test_mixed_compose_never_merges(self):
        api=Fake();api.pr['body']=UPDATES+base64.b64encode(json.dumps([{'depName':'x','newValue':'2','datasource':'npm'},{'depName':'androidx.core:core','newValue':'2','datasource':'maven'}]).encode()).decode()+' -->'
        reconcile(api,api.config);self.assertEqual(api.actions,[])
    def test_failure_one_retry_then_close_with_quarantine_and_one_comment(self):
        api=Fake();api.run['conclusion']='failure';api.jobs[0]['conclusion']='failure';api.checks[0]['conclusion']='failure'
        reconcile(api,api.config)
        self.assertEqual(len(api.comments),1)
        self.assertEqual(sum(p=='actions/runs/1/rerun-failed-jobs' for p,_ in api.actions),1)
        reconcile(api,api.config)
        self.assertEqual(sum(p=='actions/runs/1/rerun-failed-jobs' for p,_ in api.actions),1)
        api.run['run_attempt']=2;reconcile(api,api.config)
        self.assertEqual(api.pr['state'],'closed');self.assertEqual(len(api.comments),1)
        self.assertTrue(api.policy['packageRules'][0]['dependencyDashboardApproval'])
        self.assertLess(next(i for i,x in enumerate(api.actions) if x[0]=='policy'), next(i for i,x in enumerate(api.actions) if x[0]=='pulls/1'))
        reconcile(api,api.config);self.assertEqual(len(api.comments),1)
    def test_successful_retry_merges(self):
        api=Fake();api.comments=[{'id':10,'user':{'login':'controller'},'body':MARKER+json.dumps({'sha':'abc','run_id':1,'attempt':1})+' -->'}]
        api.run['run_attempt']=2;reconcile(api,api.config)
        self.assertTrue(any(p=='pulls/1/merge' for p,_ in api.actions))
    def test_stale_event_or_pending_or_human_or_missing_or_unprotected_waits(self):
        for update in (lambda a:a.run.update(status='in_progress'),lambda a:a.commits.append({'author':{'id':1}}),lambda a:a.jobs.clear(),lambda a:setattr(a,'protected',False)):
            api=Fake();update(api);reconcile(api,api.config);self.assertEqual(api.actions,[])
        api=Fake();reconcile(api,api.config,dict(api.run,run_attempt=0));self.assertEqual(api.actions,[])
    def test_failed_rerun_api_keeps_claim_and_explains_manual_recovery(self):
        import urllib.error
        api=Fake();api.run['conclusion']='failure';api.jobs[0]['conclusion']='failure'
        original=api.request
        def request(method,path,data=None):
            if path.endswith('rerun-failed-jobs'):
                raise urllib.error.URLError('test outage')
            return original(method,path,data)
        api.request=request
        with self.assertRaises(urllib.error.URLError):reconcile(api,api.config)
        self.assertEqual(len(api.comments),1)
        self.assertIn('Re-run failed jobs',api.comments[0]['body'])
        self.assertIn('run_id',api.comments[0]['body'])
        reconcile(api,api.config)
        self.assertEqual(len(api.comments),1)

    def test_current_target_is_persisted_once(self):
        api=Fake();api.target='release/0.3.0';reconcile(api,api.config)
        self.assertEqual(api.policy['baseBranchPatterns'],['release/0.3.0'])
        self.assertEqual(len(api.actions),1)

if __name__=='__main__':unittest.main()
