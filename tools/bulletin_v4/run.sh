#!/usr/bin/env bash
set -euo pipefail
test "${GITHUB_REF:-}" = refs/heads/main
test "${GITHUB_EVENT_NAME:-}" = workflow_dispatch
[[ ${GITHUB_RUN_ID:-} =~ ^[0-9]+$ ]]
[[ ${GITHUB_RUN_ATTEMPT:-} =~ ^[0-9]+$ ]]
[[ ${V4_IMAGE:-} =~ ^"${ACR_LOGIN_SERVER:?}/${V4_IMAGE_REPOSITORY:?}"@sha256:[a-f0-9]{64}$ ]]
run_id="${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"
db_name="hhc-v4-db-${run_id}"
job_name="hhc-v4-job-${run_id}"
subscription="${AZURE_SUBSCRIPTION_ID:?}"
: "${RESOURCE_GROUP:?}" "${CONTAINER_APP_ENVIRONMENT_NAME:?}" "${V4_BASE_IMAGE:?}"
test "$(az account show --query id -o tsv)" = "$subscription"
test "$(az containerapp job show -g "$RESOURCE_GROUP" -n hhc-web-bulletin-watermark --query 'properties.template.containers[0].image' -o tsv)" = "$V4_BASE_IMAGE"
mkdir -p v4-evidence
scratch=$(mktemp -d)
chmod 700 "$scratch"
cleanup_enabled=false
cleanup() {
  local status=$? failed=0
  trap - EXIT
  for pair in "jobs:$job_name" "containerApps:$db_name"; do
    if test "$cleanup_enabled" != true; then break; fi
    local kind=${pair%%:*} name=${pair#*:}
    local resource="/subscriptions/$subscription/resourceGroups/$RESOURCE_GROUP/providers/Microsoft.App/$kind/$name"
    # List only public ownership tags; never request configuration/secrets.
    if ! az resource list -g "$RESOURCE_GROUP" --resource-type "Microsoft.App/$kind" --query "[?name=='$name'].{id:id,tags:tags}" -o json > "$scratch/owner.json"; then failed=1; continue; fi
    if ! python3 - "$scratch/owner.json" "$run_id" <<'PY'
import json,sys
items=json.load(open(sys.argv[1]))
if not items: sys.exit(3)
assert len(items)==1
t=items[0]['tags']
assert t.get('owner')=='hhc-web-api' and t.get('task')=='V4-405' and t.get('temporary')=='true' and t.get('run')==sys.argv[2]
PY
    then
      if test "$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))))' "$scratch/owner.json")" = 0; then continue; fi
      failed=1
      continue
    fi
    if ! az resource delete --ids "$resource" --only-show-errors; then failed=1; continue; fi
    if test "$(az resource list -g "$RESOURCE_GROUP" --resource-type "Microsoft.App/$kind" --query "[?name=='$name'] | length(@)" -o tsv)" != 0; then failed=1; fi
  done
  rm -rf "$scratch"
  printf '{"cleanupSucceeded":%s}\n' "$(test "$failed" = 0 && echo true || echo false)" > v4-evidence/cleanup.json
  if test "$failed" != 0; then exit 1; fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Do not touch a pre-existing resource, even one with a familiar name.
for name in "$db_name" "$job_name"; do
  test "$(az resource list -g "$RESOURCE_GROUP" --query "[?name=='$name'] | length(@)" -o tsv)" = 0
done
cleanup_enabled=true
password=$(python3 -c 'import secrets; print(secrets.token_hex(24))')
echo "::add-mask::$password"
export V4_RUN_ID="$run_id" V4_PASSWORD="$password"
python3 - "$scratch/parameters.json" <<'PY'
import json,os,sys
data={k:{'value':os.environ[v]} for k,v in [('runId','V4_RUN_ID'),('password','V4_PASSWORD'),('image','V4_IMAGE')]}
data['labDSN']={'value':'postgres://v4_lab:'+os.environ['V4_PASSWORD']+'@hhc-v4-db-'+os.environ['V4_RUN_ID']+':5432/v4_lab?sslmode=disable'}
data['containerAppEnvironmentName']={'value':os.environ['CONTAINER_APP_ENVIRONMENT_NAME']}
data['containerRegistryLoginServer']={'value':os.environ['ACR_LOGIN_SERVER']}
with open(sys.argv[1],'w') as f: json.dump({'parameters':data},f)
PY
chmod 600 "$scratch/parameters.json"
unset V4_PASSWORD password
az deployment group what-if -g "$RESOURCE_GROUP" --template-file infra/bulletin-v4.bicep --parameters "@$scratch/parameters.json" --validation-level Provider --result-format ResourceIdOnly --no-pretty-print -o json > "$scratch/preview.json"
python3 tools/bulletin_v4/preview.py "$scratch/preview.json" "$subscription" "$run_id" "$RESOURCE_GROUP" > v4-evidence/preview.json
cat v4-evidence/preview.json
az deployment group create -g "$RESOURCE_GROUP" -n "v4-lab-$run_id" --template-file infra/bulletin-v4.bicep --parameters "@$scratch/parameters.json" --query properties.provisioningState -o tsv | grep -Fx Succeeded
az containerapp show -g "$RESOURCE_GROUP" -n "$db_name" --query '{external:properties.configuration.ingress.external,transport:properties.configuration.ingress.transport,port:properties.configuration.ingress.targetPort,min:properties.template.scale.minReplicas,max:properties.template.scale.maxReplicas}' -o json > v4-evidence/database.json
python3 tools/bulletin_v4/preview.py --database v4-evidence/database.json
az containerapp job show -g "$RESOURCE_GROUP" -n "$job_name" --query '{name:name,identity:identity.type,trigger:properties.configuration.triggerType,timeout:properties.configuration.replicaTimeout,retry:properties.configuration.replicaRetryLimit,manual:properties.configuration.manualTriggerConfig,containers:properties.template.containers[].{image:image,resources:resources}}' -o json > "$scratch/job.json"
python3 - "$scratch/job.json" "$V4_IMAGE" <<'PY'
import json,sys
j=json.load(open(sys.argv[1]))
assert j['trigger']=='Manual' and j['timeout']==300 and j['retry']==0
assert j['manual']=={'parallelism':20,'replicaCompletionCount':20}
assert len(j['containers'])==1 and j['containers'][0]['image']==sys.argv[2]
r=j['containers'][0]['resources']
assert r['cpu']==2 and r['memory']=='4Gi'
with open('v4-evidence/job.json','w') as f:json.dump({'verified':True,'imageDigest':sys.argv[2].rsplit('@',1)[1]},f)
PY
execution=$(az containerapp job start -g "$RESOURCE_GROUP" -n "$job_name" --query name -o tsv)
for iteration in $(seq 1 84); do
  az containerapp job execution show -g "$RESOURCE_GROUP" -n "$job_name" --job-execution-name "$execution" --query '{name:name,start:properties.startTime,end:properties.endTime,status:properties.status}' -o json > v4-evidence/execution.json
  state=$(python3 -c 'import json;print(json.load(open("v4-evidence/execution.json"))["status"])')
  if test "$state" = Succeeded; then cat v4-evidence/execution.json; exit 0; fi
  if test "$state" = Failed; then cat v4-evidence/execution.json; exit 1; fi
  sleep 5
done
echo 'Finite V4 execution did not finish within its observation budget' >&2
exit 1
