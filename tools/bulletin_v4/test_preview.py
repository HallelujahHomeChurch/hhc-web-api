import unittest
from preview import validate, validate_database


class PreviewTest(unittest.TestCase):
    def test_database_protocol_case_preserves_private_scope(self):
        data = {"external": False, "transport": "Tcp", "port": 5432, "min": 1, "max": 1}
        for protocol in ("Tcp", "tcp", "TCP"):
            validate_database(dict(data, transport=protocol))
        for field, value in (("external", True), ("external", None), ("external", 0),
                             ("transport", "Http"), ("transport", None),
                             ("port", 80), ("min", 0), ("max", 2)):
            with self.assertRaises(ValueError):
                validate_database(dict(data, **{field: value}))

    def test_exact_create_only(self):
        base = "/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/"
        changes = [{"changeType": "Create", "resourceId": base + suffix}
                   for suffix in ("containerApps/hhc-v4-db-123", "jobs/hhc-v4-job-123")]
        self.assertEqual(len(validate({"status":"Succeeded","changes": changes}, "test", "123", "example-group")), 2)
        for extra in ({"changeType": "Modify", "resourceId": base + "containerApps/asset-api"},
                      {"changeType": "Delete", "resourceId": changes[0]["resourceId"]},
                      {"changeType": "Create", "resourceId": base + "jobs/other"}):
            with self.assertRaises(ValueError):
                validate({"status":"Succeeded","changes": changes + [extra]}, "test", "123", "example-group")
        with self.assertRaises(ValueError):
            validate({"status":"Succeeded","changes": changes[:1]}, "test", "123", "example-group")
        with self.assertRaises(ValueError):
            validate({"status":"Failed","changes": changes}, "test", "123", "example-group")
