import json
from pathlib import Path
import re
import unittest
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

if __name__=='__main__':unittest.main()
