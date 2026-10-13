import json
from pathlib import Path
import re
import unittest
from unittest.mock import patch
import tempfile
import urllib.request
from archive_service_evidence import store, NoCredentialRedirect
from service_e2e import passed_tests, validate_plan

ROOT = Path(__file__).resolve().parents[2]

class EvidenceGuards(unittest.TestCase):
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
    def test_redirect_drops_cross_host_authentication(self):
        req=urllib.request.Request("https://api.github.com/assets/1",headers={"Authorization":"Bearer synthetic"})
        redirected=NoCredentialRedirect().redirect_request(req,None,302,"",{},"https://release-assets.githubusercontent.com/file")
        self.assertIsNone(redirected.get_header("Authorization"))
        with self.assertRaises(ValueError):NoCredentialRedirect().redirect_request(req,None,302,"",{},"http://example.test/file")

    def test_incomplete_existing_archive_fails_without_mutation(self):
        sha,digest="a"*40,"b"*64
        name=f"development-evidence.{sha}.service.before-build.json"
        document={"sourceSha":sha,"repository":"seorilabs/platform","e2e":{"environment":{"configurationFingerprint":"c"*64},"completedAt":"2026-10-13T00:00:00Z"},"acceptance":[{"evidence":[f"runtime-result.{sha}.{digest}.json"]}]}
        def fixture(path,token,body=None,binary=False):
            self.assertIsNone(body,"existing incomplete archive must not be mutated")
            if "/releases?" in path:return [{"id":1,"draft":True,"tag_name":"development-evidence"}]
            if "/assets?" in path:return [{"id":2,"name":name,"url":"https://api.github.com/assets/2"}]
            return json.dumps(document).encode()
        with tempfile.TemporaryDirectory() as folder:
            folder=Path(folder)
            (folder/name).write_text(json.dumps(document))
            (folder/"runtime-result.json").write_text(json.dumps({"rawLogSha256":digest}))
            (folder/"test-events.json").write_text("[]")
            with patch.dict("os.environ",{"GH_TOKEN":"synthetic"}),patch("archive_service_evidence.request",side_effect=fixture):
                with self.assertRaisesRegex(ValueError,"incomplete canonical"):store(folder)

if __name__=='__main__':unittest.main()
