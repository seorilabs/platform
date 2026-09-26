import contextlib
import io
import unittest
from unittest.mock import patch
import check_reascend_apple_runtime as audit


class RuntimeAuditTests(unittest.TestCase):
    def fixtures(self):
        service = {"spec": {"template": {"spec": {"containers": [{"env": [
            {"name": "IAP_APPLE_KEY_ID", "value": "4N8V928LWA"},
            {"name": "IAP_APPLE_ISSUER_ID", "value": "69a6de86-a179-47e3-e053-5b8c7c11a4d1"},
            {"name": "IAP_APPLE_KEY", "value": "private-material-must-not-appear"}
        ]}]}}}}
        catalog = {"apps": {"reascend": {"entitlements": {
            "gems_" + suffix: {"type": "consumable", "app_store": "com.seorilabs.reascend.gems." + suffix}
            for suffix in ("starter", "small", "medium", "large")}}}}
        return service, catalog

    def run_audit(self, service, catalog):
        output = io.StringIO()
        with patch.object(audit, "gcloud_json", side_effect=[service, catalog]), patch.dict(audit.os.environ, {"REGION": "test"}), contextlib.redirect_stdout(output):
            audit.main()
        return output.getvalue()

    def test_expected_key_without_private_material(self):
        output = self.run_audit(*self.fixtures())
        self.assertIn("4N8V928LWA", output)
        self.assertNotIn("private-material", output)

    def test_failed_read_reports_only_failure_category(self):
        response = audit.subprocess.CompletedProcess([], 1, "private-stdout", "PERMISSION_DENIED private-stderr")
        with patch.object(audit.subprocess, "run", return_value=response), patch.dict(audit.os.environ, {"PROJECT_ID": "test"}):
            with self.assertRaises(SystemExit) as failure:
                audit.gcloud_json("secrets", "versions", "access")
        self.assertIn("PERMISSION_DENIED", str(failure.exception))
        self.assertNotIn("private-", str(failure.exception))

    def test_wrong_key_blocks_before_catalog_read(self):
        service, catalog = self.fixtures()
        service["spec"]["template"]["spec"]["containers"][0]["env"][0]["value"] = "OTHERKEY"
        with self.assertRaises(SystemExit):
            self.run_audit(service, catalog)


if __name__ == "__main__":
    unittest.main()
