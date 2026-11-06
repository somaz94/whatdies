#!/usr/bin/env bash
# Runs the commands demo.tape shows against a real kind cluster, helm and Terraform,
# and saves their output. The recording container has none of these tools, so the
# tape replays these files. Run this again whenever a verdict or its wording changes.
if [ -n "${ZSH_VERSION:-}" ]; then
  exec bash "$0" "$@"
fi
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
whatdies=${WHATDIES:-$here/../../bin/whatdies}
out=$here/captured
cluster="whatdies-demo"
# The cleanup below deletes this name, so never adopt a cluster this run did not create.
if kind get clusters 2>/dev/null | grep -qx "$cluster"; then
  echo "a kind cluster named $cluster already exists; delete it or rename it first" >&2
  exit 1
fi

work=$(mktemp -d)
# Steps replace $out only once all have the expected status, so a failed run never
# leaves a wrong or stale step for replay.sh to show.
stage=$work/captured

# kind would otherwise switch the current context of the default kubeconfig.
export KUBECONFIG="$work/kubeconfig"

cleanup() {
  kind delete cluster --name "$cluster" --kubeconfig "$KUBECONFIG" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

# Saves step $1's output and exit status for replay.sh, and stops unless the exit
# status is $2, since a setting in this shell can turn a deny into an ask and the
# recording would then tell the wrong story.
save() {
  echo "$3" >"$stage/$1.rc"
  if [ "$3" != "$2" ]; then
    cat "$stage/$1.txt" >&2
    echo "step $1 exited $3, want $2; $out is unchanged" >&2
    exit 1
  fi
}

# check exits 3 on ask and 4 on deny. The output goes to a file, so colour and the
# recording terminal's width are asked for.
capture() {
  local rc=0
  "$whatdies" check --color always --width 120 "$3" >"$stage/$1.txt" || rc=$?
  save "$1" "$2" "$rc"
}

# Codex reads a block from stderr and exit 2. Its reason is one long line, folded at
# spaces to the recording width so the terminal does not break it inside a word.
capture_codex() {
  local rc=0
  "$whatdies" hook --codex <<<"$2" >"$work/hook.out" 2>&1 || rc=$?
  fold -s -w 120 "$work/hook.out" >"$stage/$1.txt"
  save "$1" 2 "$rc"
}

mkdir "$stage"
kind create cluster --name "$cluster" --kubeconfig "$KUBECONFIG" --wait 120s >/dev/null
kubectl apply -f "$here/k8s/shop.yaml" >/dev/null
kubectl -n shop wait pod/db --for=condition=Ready --timeout=180s >/dev/null
# helm's own homes go to $work, so nothing is written to the user's helm settings.
# HELM_* is dropped: HELM_KUBEAPISERVER and HELM_KUBETOKEN would take helm past $KUBECONFIG.
while IFS= read -r v; do unset "$v"; done < <(env | sed -n 's/^\(HELM_[A-Za-z0-9_]*\)=.*/\1/p')
HELM_CACHE_HOME="$work/helm/cache" HELM_CONFIG_HOME="$work/helm/config" HELM_DATA_HOME="$work/helm/data" \
  helm install blog "$here/helm/blog" -n blog --create-namespace --wait --timeout 180s >/dev/null

capture 1 4 "kubectl delete ns shop"
pv=$(kubectl -n shop get pvc data -o jsonpath='{.spec.volumeName}')
kubectl patch pv "$pv" -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}' >/dev/null
capture 2 3 "kubectl delete ns shop"
capture_codex 3 '{"tool_name":"Bash","tool_input":{"command":"kubectl delete ns shop"}}'
capture 4 4 "helm uninstall blog -n blog"

cp "$here/tf/main.tf" "$work/main.tf"
cd "$work"
terraform init -input=false >/dev/null
terraform apply -auto-approve -input=false -var engine=15 >/dev/null
terraform plan -input=false -out=tfplan -var engine=16 >/dev/null
capture 5 4 "terraform apply -auto-approve"
capture 6 3 "terraform apply tfplan"

# The bucket is planned offline: a pushed state says it exists with the guard on,
# and fake credentials with -refresh=false keep the provider from calling AWS.
# AWS_* and ~/.aws are kept out so no real profile reaches the provider.
mkdir "$work/bucket"
cp "$here/bucket/main.tf" "$work/bucket/main.tf"
cd "$work/bucket"
while IFS= read -r v; do unset "$v"; done < <(env | sed -n 's/^\(AWS_[A-Za-z0-9_]*\)=.*/\1/p')
: >"$work/aws-config"
export AWS_CONFIG_FILE="$work/aws-config" AWS_SHARED_CREDENTIALS_FILE="$work/aws-config" AWS_EC2_METADATA_DISABLED=true
terraform init -input=false >/dev/null
printf '%s' '{"version":4,"terraform_version":"1.0.0","serial":1,"lineage":"demo","outputs":{},"resources":[{"mode":"managed","type":"aws_s3_bucket","name":"invoices","provider":"provider[\"registry.terraform.io/hashicorp/aws\"]","instances":[{"schema_version":0,"attributes":{"id":"example-invoices","bucket":"example-invoices","force_destroy":false,"region":"us-east-1","tags":null,"tags_all":null},"sensitive_attributes":[]}]}]}' >state.json
terraform state push state.json >/dev/null
terraform plan -input=false -refresh=false -out=bucket.tfplan >/dev/null
capture 7 3 "terraform apply bucket.tfplan"

rm -rf "${out:?}"
mv "$stage" "$out"
