#!/usr/bin/env bash
# Shows the same command getting opposite verdicts as live state changes.
# Needs kind, kubectl, terraform and a running container engine.
# Touches nothing outside a temp directory and its own kind cluster.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
whatdies=${WHATDIES:-$here/../../bin/whatdies}
cluster="whatdies-demo"
work=$(mktemp -d)

# kind switches the current context of the default kubeconfig, which on a machine
# with real clusters would point later commands at the wrong one.
export KUBECONFIG="$work/kubeconfig"

cleanup() {
  kind delete cluster --name "$cluster" --kubeconfig "$KUBECONFIG" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

title() { printf '\n== %s\n' "$1"; }

# check exits non-zero on ask and deny, which is the point of the demo.
check() {
  printf '$ %s\n' "$*"
  "$whatdies" check "$@" || true
}

title "Terraform: the same command, judged by what the plan does"
cp "$here/tf/main.tf" "$work/main.tf"
cd "$work"
terraform init -input=false >/dev/null
terraform apply -auto-approve -input=false -var engine=15 >/dev/null
tfdata=(--stateful terraform_data)

terraform plan -input=false -out=tfplan -var engine=15 -var note=second >/dev/null
check "${tfdata[@]}" "terraform apply tfplan"
terraform plan -input=false -out=tfplan -var engine=16 >/dev/null
check "${tfdata[@]}" "terraform apply tfplan"
check "terraform apply -auto-approve"
check "terraform plan -out=tfplan && terraform apply tfplan"

title "Kubernetes: the same command, judged by the volumes behind it"
kind create cluster --name "$cluster" --kubeconfig "$KUBECONFIG" --wait 120s >/dev/null
kubectl apply -f "$here/k8s/shop.yaml" -f "$here/k8s/scratch.yaml" >/dev/null
kubectl -n shop wait pod/db --for=condition=Ready --timeout=180s >/dev/null

check "kubectl delete ns scratch"
check "kubectl delete ns shop"
pv=$(kubectl -n shop get pvc data -o jsonpath='{.spec.volumeName}')
kubectl patch pv "$pv" -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}' >/dev/null
printf '\n# reclaim policy changed to Retain; the command below is identical\n'
check "kubectl delete ns shop"

title "The hook path an agent goes through"
payload=$(printf '{"tool_name":"Bash","cwd":"%s","tool_input":{"command":"kubectl delete ns scratch"}}' "$work")
printf '%s' "$payload" | "$whatdies" hook
