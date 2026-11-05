#!/usr/bin/env bash
# Runs the commands demo.tape shows against a real kind cluster and Terraform, and
# saves their output. The recording container has neither tool, so the tape replays
# these files. Run this again whenever a verdict or its wording changes.
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

# kind would otherwise switch the current context of the default kubeconfig.
export KUBECONFIG="$work/kubeconfig"

cleanup() {
  kind delete cluster --name "$cluster" --kubeconfig "$KUBECONFIG" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

# check exits non-zero on ask and deny, which is what is being captured.
capture() { "$whatdies" check "$2" >"$out/$1.txt" || true; }

mkdir -p "$out"
kind create cluster --name "$cluster" --kubeconfig "$KUBECONFIG" --wait 120s >/dev/null
kubectl apply -f "$here/k8s/shop.yaml" >/dev/null
kubectl -n shop wait pod/db --for=condition=Ready --timeout=180s >/dev/null

capture 1 "kubectl delete ns shop"
pv=$(kubectl -n shop get pvc data -o jsonpath='{.spec.volumeName}')
kubectl patch pv "$pv" -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}' >/dev/null
capture 2 "kubectl delete ns shop"

cp "$here/tf/main.tf" "$work/main.tf"
cd "$work"
terraform init -input=false >/dev/null
terraform apply -auto-approve -input=false -var engine=15 >/dev/null
terraform plan -input=false -out=tfplan -var engine=16 >/dev/null
capture 3 "terraform apply -auto-approve"
capture 4 "terraform apply tfplan"
