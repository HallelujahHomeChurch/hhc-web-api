"""Reject every mutation except creation of the two exact owned lab resources."""
import json
import sys


def validate_database(data):
    if (data.get("external") is not False
            or str(data.get("transport", "")).lower() != "tcp"
            or [data.get(key) for key in ("port", "min", "max")] != [5432, 1, 1]):
        raise ValueError("V4 database must remain private TCP5432 at exactly one replica")


def validate(data, subscription, run_id, resource_group):
    if data.get("status") != "Succeeded" or data.get("error"):
        raise ValueError("V4 Provider preview did not succeed")
    prefix = f"/subscriptions/{subscription}/resourceGroups/{resource_group}/providers/Microsoft.App/"
    expected = {
        (prefix + "containerApps/hhc-v4-db-" + run_id).lower(),
        (prefix + "jobs/hhc-v4-job-" + run_id).lower(),
    }
    created = set()
    for change in data["changes"]:
        if change["changeType"] in ("Ignore", "NoChange"):
            continue
        resource = change["resourceId"].lower()
        if change["changeType"] != "Create" or resource not in expected:
            raise ValueError("unexpected resource mutation in V4 preview")
        created.add(resource)
    if created != expected:
        raise ValueError("V4 preview must create exactly the database and finite Job")
    return sorted(created)


if __name__ == "__main__":
    if sys.argv[1] == "--database":
        with open(sys.argv[2]) as stream:
            validate_database(json.load(stream))
        print(json.dumps({"privateDatabaseVerified": True}))
    else:
        with open(sys.argv[1]) as stream:
            resources = validate(json.load(stream), sys.argv[2], sys.argv[3], sys.argv[4])
        print(json.dumps({"allowedCreateCount": len(resources), "existingResourceMutations": 0}))
