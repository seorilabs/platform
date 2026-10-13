import json
import copy
from pathlib import Path
import re
import unittest
from unittest.mock import patch
import tempfile
import urllib.request
from urllib.parse import urlsplit, parse_qs
from archive_service_evidence import store, request, validate_bundle, NoCredentialRedirect
from service_e2e import passed_tests, validate_plan, preserve_failure

ROOT = Path(__file__).resolve().parents[2]

class EvidenceGuards(unittest.TestCase):
    def test_failed_execution_preserves_only_metadata_and_cannot_unlock_build(self):
        with tempfile.TemporaryDirectory() as directory:
            directory=Path(directory)
            raw=directory/'private-raw.jsonl'
            raw.write_text(json.dumps({'Action':'fail','Package':'synthetic/package','Test':'TestFailed','Output':'synthetic-secret'})+'\n'+'unstructured synthetic-secret\n')
            output=directory/'public'
            output.mkdir()
            with patch('builtins.print'):preserve_failure(raw,output,'a'*40,1)
            result=json.loads((output/'failure-result.json').read_text())
            self.assertEqual(result['status'],'failed')
            self.assertEqual(result['failed'][0]['Test'],'TestFailed')
            self.assertEqual(result['unstructuredLines'],1)
            self.assertNotIn('synthetic-secret',''.join(p.read_text() for p in output.iterdir()))
            self.assertFalse(list(output.glob('development-evidence.*.json')))
            self.assertFalse((output/'runtime-result.json').exists())
            with self.assertRaisesRegex(ValueError,'failed or skipped'):
                passed_tests([json.dumps(e) for e in json.loads((output/'test-events.json').read_text())],{'TestFailed'})

    def test_inventory_covers_source_routes(self):
        inventory=json.loads((ROOT/'docs/qa/feature-inventory.json').read_text())
        plan=json.loads((ROOT/'docs/qa/service-e2e-plan.json').read_text())
        routes=re.findall(r'^  (/[^\s:][^\n]*):$',(ROOT/'spec/openapi.yaml').read_text(),re.M)
        validate_plan(inventory,plan,routes)
        inventory['apiRoutes'].pop()
        with self.assertRaisesRegex(ValueError,'route inventory'):validate_plan(inventory,plan,routes)
    def test_missing_feature_fails(self):
        with self.assertRaisesRegex(ValueError,'unmapped'):
            validate_plan({'features':[{'id':'a'},{'id':'b'}],'apiRoutes':[]},{'scenarios':[{'featureIds':['a'],'requiredTests':['TestRuntime']}]},[])
    def test_skip_failure_and_zero_execution_rejected(self):
        for action in ['skip','fail']:
            with self.assertRaises(ValueError):passed_tests([json.dumps({'Action':action,'Test':'TestRuntime'})],{'TestRuntime'})
        with self.assertRaisesRegex(ValueError,'did not execute'):passed_tests([],{'TestRuntime'})
    def test_sanitized_events_exclude_token_output(self):
        safe,_=passed_tests([json.dumps({'Action':'output','Test':'TestRuntime','Output':'synthetic-secret'}),json.dumps({'Action':'pass','Test':'TestRuntime'})],{'TestRuntime'})
        self.assertNotIn('synthetic-secret',json.dumps(safe))

class ArchiveSafety(unittest.TestCase):
    def bundle(self):
        sha,digest,fp="a"*40,"b"*64,"c"*64
        plan=json.loads((ROOT/'docs/qa/service-e2e-plan.json').read_text())
        required={t for s in plan['scenarios'] for t in s['requiredTests']}
        events=[{'Action':'pass','Test':name} for name in sorted(required)]
        times={'plannedAt':'2026-10-13T00:00:00Z','startedAt':'2026-10-13T00:00:01Z','completedAt':'2026-10-13T00:00:02Z'}
        ref=f'runtime-result.{sha}.{digest}.json'
        document={'sourceSha':sha,'repository':'seorilabs/platform','e2e':{'sourceSha':sha,'environment':{'configurationFingerprint':fp},'scenarios':[{'evidence':[ref]}],**times},'acceptance':[{'evidence':[ref]}]}
        runtime={'sourceSha':sha,'configurationFingerprint':fp,'rawLogSha256':digest,'counts':{'pass':len(events),'skip':0,'fail':0},**times}
        return document,runtime,events

    def test_initial_origin_is_checked_before_network(self):
        for url in ['https://outside.example.test/file','http://api.github.com/file','//outside.example.test/file','https://api.github.com:8443/file','https://synthetic@api.github.com/file','https://api.github.com/file#fragment']:
            with self.subTest(url=url),patch('urllib.request.build_opener') as opener:
                with self.assertRaisesRegex(ValueError,'origin'):request(url,'synthetic')
                opener.assert_not_called()
        for url in ['/repos/seorilabs/platform','https://api.github.com/assets/1','https://uploads.github.com/repos/seorilabs/platform/assets']:
            with patch('urllib.request.build_opener') as opener:
                opener.return_value.open.return_value.__enter__.return_value.read.return_value=b'{}'
                self.assertEqual(request(url,'synthetic'),{})
                opener.assert_called_once()

    def test_bundle_rejects_mixed_identity_and_execution(self):
        document,runtime,events=self.bundle()
        self.assertEqual(validate_bundle(document,runtime,events),(document['sourceSha'],runtime['rawLogSha256']))
        for key in ['sourceSha','configurationFingerprint','plannedAt','startedAt','completedAt','rawLogSha256','counts']:
            changed=copy.deepcopy(runtime)
            changed[key]='d'*64 if key!='counts' else {'pass':0,'skip':0,'fail':0}
            with self.subTest(key=key),self.assertRaises(ValueError):validate_bundle(document,changed,events)
        for invalid in [[],events[:-1],events+[{'Action':'skip','Test':'TestSkipped'}],events+[{'Action':'fail','Test':'TestFailed'}],events+[{'Action':'output','Output':'synthetic-only'}]]:
            with self.assertRaises(ValueError):validate_bundle(document,runtime,invalid)

    def test_invalid_new_bundle_never_reaches_archive(self):
        document,runtime,events=self.bundle()
        for change in ['source','missing-event','reference']:
            d,r,e=copy.deepcopy((document,runtime,events))
            if change=='source':r['sourceSha']='d'*40
            elif change=='missing-event':e.pop()
            else:d['acceptance'][0]['evidence']=['unrelated.json']
            with tempfile.TemporaryDirectory() as folder:
                folder=Path(folder)
                (folder/f"development-evidence.{d['sourceSha']}.service.before-build.json").write_text(json.dumps(d))
                (folder/'runtime-result.json').write_text(json.dumps(r))
                (folder/'test-events.json').write_text(json.dumps(e))
                with patch('archive_service_evidence.request') as api:
                    with self.assertRaises(ValueError):store(folder)
                    api.assert_not_called()

    def test_new_archive_is_complete_before_canonical_and_reuse_is_readonly(self):
        document,runtime,events=self.bundle()
        sha,digest=document['sourceSha'],runtime['rawLogSha256']
        canonical=f'development-evidence.{sha}.service.before-build.json'
        names=[f'runtime-result.{sha}.{digest}.json',f'test-events.{sha}.{digest}.json',canonical]
        assets={}
        uploads=[]
        def fixture(path,token,body=None,binary=False):
            if path.startswith('https://uploads.github.com/'):
                name=parse_qs(urlsplit(path).query)['name'][0]
                if name==canonical:self.assertEqual(list(assets),names[:2])
                idx=len(assets)+1
                assets[name]={'id':idx,'name':name,'url':f'https://api.github.com/assets/{idx}','raw':body}
                uploads.append(name)
                return {}
            if '/releases?' in path:return [{'id':1,'draft':True,'tag_name':'development-evidence','upload_url':'https://uploads.github.com/repos/seorilabs/platform/releases/1/assets{?name,label}'}]
            if '/assets?' in path:return [{k:v for k,v in value.items() if k!='raw'} for value in assets.values()]
            self.assertIsNone(body)
            return next(value['raw'] for value in assets.values() if value['url']==path)
        with tempfile.TemporaryDirectory() as folder:
            folder=Path(folder)
            (folder/canonical).write_text(json.dumps(document))
            (folder/'runtime-result.json').write_text(json.dumps(runtime))
            (folder/'test-events.json').write_text(json.dumps(events))
            with patch.dict('os.environ',{'GH_TOKEN':'synthetic'}),patch('archive_service_evidence.request',side_effect=fixture):
                store(folder)
                self.assertEqual(uploads,names)
                store(folder)
                self.assertEqual(uploads,names,'reuse must not mutate archive')
                bad=copy.deepcopy(runtime)
                bad['configurationFingerprint']='d'*64
                assets[names[0]]['raw']=json.dumps(bad).encode()
                with self.assertRaisesRegex(ValueError,'configuration'):store(folder)
                self.assertEqual(uploads,names,'inconsistent readback must not mutate archive')

    def test_redirect_drops_cross_host_authentication(self):
        req=urllib.request.Request("https://api.github.com/assets/1",headers={"Authorization":"Bearer synthetic"})
        redirected=NoCredentialRedirect().redirect_request(req,None,302,"",{},"https://release-assets.githubusercontent.com/file")
        self.assertIsNone(redirected.get_header("Authorization"))
        with self.assertRaises(ValueError):NoCredentialRedirect().redirect_request(req,None,302,"",{},"http://example.test/file")

    def test_incomplete_existing_archive_fails_without_mutation(self):
        document,runtime,events=self.bundle()
        sha=document['sourceSha']
        name=f"development-evidence.{sha}.service.before-build.json"
        def fixture(path,token,body=None,binary=False):
            self.assertIsNone(body,"existing incomplete archive must not be mutated")
            if "/releases?" in path:return [{"id":1,"draft":True,"tag_name":"development-evidence"}]
            if "/assets?" in path:return [{"id":2,"name":name,"url":"https://api.github.com/assets/2"}]
            return json.dumps(document).encode()
        with tempfile.TemporaryDirectory() as folder:
            folder=Path(folder)
            (folder/name).write_text(json.dumps(document))
            (folder/"runtime-result.json").write_text(json.dumps(runtime))
            (folder/"test-events.json").write_text(json.dumps(events))
            with patch.dict("os.environ",{"GH_TOKEN":"synthetic"}),patch("archive_service_evidence.request",side_effect=fixture):
                with self.assertRaisesRegex(ValueError,"incomplete canonical"):store(folder)

if __name__=='__main__':unittest.main()
