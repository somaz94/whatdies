#!/usr/bin/env bash
# Regenerates the saved plans and their `terraform show -json` output that
# planfile_test.go compares. Needs terraform and network access for hashicorp/local and hashicorp/aws.
# Every workspace is synthetic and lives in a temporary directory.
if [ -n "${ZSH_VERSION:-}" ]; then
  exec bash "$0" "$@"
fi
set -euo pipefail

out="$(cd "$(dirname "$0")" && pwd)"
[ -f "$out/generate.sh" ] || { echo "generate.sh: run it by path, not from stdin" >&2; exit 1; }
work="$(mktemp -d)"
cleanup() { rm -rf "${work:?}"; }
trap cleanup EXIT
stage="$work/fixtures"
mkdir "$stage"

# TF_VAR_*, TF_CLI_ARGS*, TF_DATA_DIR and the user's CLI config would change the fixtures or write outside $work.
while IFS= read -r v; do unset "$v"; done < <(env | sed -n 's/^\(TF_[A-Za-z0-9_]*\)=.*/\1/p')
printf 'provider_installation {\n  direct {}\n}\n' >"$work/terraformrc"
export TF_CLI_CONFIG_FILE="$work/terraformrc" TF_IN_AUTOMATION=1 TF_INPUT=0 CHECKPOINT_DISABLE=1

# AWS_* and ~/.aws would let the user's profiles and settings reach the guards workspace's provider.
while IFS= read -r v; do unset "$v"; done < <(env | sed -n 's/^\(AWS_[A-Za-z0-9_]*\)=.*/\1/p')
: >"$work/aws-config"
: >"$work/aws-credentials"
export AWS_CONFIG_FILE="$work/aws-config" AWS_SHARED_CREDENTIALS_FILE="$work/aws-credentials" AWS_EC2_METADATA_DISABLED=true

tf() { terraform "$@" -no-color >/dev/null; }

save() {
  local name="$1" plan="$2"
  cp "$plan" "$stage/$name.tfplan"
  terraform show -no-color -json "$plan" >"$stage/$name.json"
}

workspace() {
  mkdir -p "$work/$1"
  cd "$work/$1"
}

# Update, both kinds of replace, a count removal, module and keyed addresses, a data source.
workspace changes
mkdir -p mod
cat >mod/main.tf <<'EOF'
variable "gen" {
  type = number
}

resource "terraform_data" "inner" {
  triggers_replace = var.gen
}
EOF
cat >main.tf <<'EOF'
variable "gen" {
  type    = number
  default = 1
}

resource "terraform_data" "keep" {
  input = "same"
}

resource "terraform_data" "upd" {
  input = "v${var.gen}"
}

resource "terraform_data" "repl" {
  triggers_replace = var.gen
}

resource "terraform_data" "cbd" {
  triggers_replace = var.gen
  lifecycle {
    create_before_destroy = true
  }
}

resource "terraform_data" "gone" {
  count = var.gen == 1 ? 1 : 0
}

resource "terraform_data" "keyed" {
  for_each = toset(["a.b", "c\"d] e"])
  input    = each.key
}

module "m" {
  source = "./mod"
  gen    = var.gen
}

data "terraform_remote_state" "rs" {
  backend = "local"
  config  = { path = "${path.module}/other.tfstate" }
}
EOF
printf '{"version":4,"serial":1,"lineage":"fixture","outputs":{},"resources":[]}' >other.tfstate
tf init
tf apply -auto-approve
tf plan -var gen=2 -out=p
save changes p
tf plan -destroy -out=p
save destroy p
tf plan -refresh-only -out=p
save refresh-only p
tf plan -out=p
save no-change p

# forget, through a removed block.
workspace forget
printf 'resource "terraform_data" "a" {\n  input = 1\n}\n' >main.tf
tf init
tf apply -auto-approve
printf 'removed {\n  from = terraform_data.a\n  lifecycle {\n    destroy = false\n  }\n}\n' >main.tf
tf plan -out=p
save forget p

# A deposed object, planned with -target, which makes the plan partial.
workspace deposed
cat >main.tf <<'EOF'
resource "terraform_data" "d" {
  lifecycle {
    create_before_destroy = true
  }
}

resource "terraform_data" "other" {}
EOF
tf init
instance() {
  printf '{"schema_version":0,"attributes":{"id":"%s","input":null,"output":null,"triggers_replace":null},"sensitive_attributes":[],"create_before_destroy":true%s}' "$1" "$2"
}
printf '{"version":4,"terraform_version":"1.0.0","serial":1,"lineage":"fixture","outputs":{},"resources":[{"mode":"managed","type":"terraform_data","name":"d","provider":"provider[\\"terraform.io/builtin/terraform\\"]","instances":[%s,%s]}]}' \
  "$(instance 11111111-1111-1111-1111-111111111111 '')" \
  "$(instance 22222222-2222-2222-2222-222222222222 ',"deposed":"0f0f0f0f"')" >state.json
tf state push state.json
tf plan -target=terraform_data.d -out=p
save deposed-targeted p

# A data source read at apply time, and a real provisioner.
workspace provisioner
cat >main.tf <<'EOF'
resource "terraform_data" "src" {}

data "terraform_remote_state" "late" {
  backend = "local"
  config  = { path = "${path.module}/x${terraform_data.src.id}.tfstate" }
}

resource "terraform_data" "p" {
  provisioner "local-exec" {
    command = "true"
  }
}
EOF
tf init
tf plan -out=p
save provisioner p

# A failed precondition: the plan is saved but marked errored.
workspace errored
cat >main.tf <<'EOF'
variable "x" {
  type    = number
  default = 1
}

resource "terraform_data" "bad" {
  lifecycle {
    precondition {
      condition     = var.x == 2
      error_message = "fixture"
    }
  }
}
EOF
tf init
if terraform plan -no-color -out=p >/dev/null 2>&1; then
  echo "errored: terraform plan succeeded; the precondition should have failed" >&2
  exit 1
fi
[ -f p ] || { echo "errored: terraform plan failed without saving a plan" >&2; exit 1; }
case "$(terraform show -no-color -json p)" in
  *'"errored":true'*) ;;
  *) echo "errored: the saved plan is not marked errored" >&2; exit 1 ;;
esac
save errored p

# Drift, moved, import, checks, outputs and a sensitive variable, which add
# top-level fields that must not make a verdict stricter.
workspace rich
cat >versions.tf <<'EOF'
terraform {
  required_providers {
    local = { source = "hashicorp/local", version = "2.9.1" }
  }
}
EOF
cat >main.tf <<'EOF'
resource "local_file" "f" {
  filename = "${path.module}/f.txt"
  content  = "fixture"
}

resource "terraform_data" "old" {
  input = 1
}
EOF
tf init
tf apply -auto-approve
rm f.txt
cat >main.tf <<'EOF'
variable "secret" {
  type      = string
  default   = "fixture-secret"
  sensitive = true
}

resource "local_file" "f" {
  filename = "${path.module}/f.txt"
  content  = "fixture"
}

moved {
  from = terraform_data.old
  to   = terraform_data.new
}

resource "terraform_data" "new" {
  input = 2
}

import {
  to = terraform_data.imp
  id = "fixture"
}

resource "terraform_data" "imp" {}

check "c" {
  assert {
    condition     = var.secret != ""
    error_message = "fixture"
  }
}

output "o" {
  value = terraform_data.new.output
}

output "s" {
  value     = var.secret
  sensitive = true
}
EOF
tf plan -out=p
save rich p

# A provider action, triggered after a create.
workspace action
cp ../rich/versions.tf .
cat >main.tf <<'EOF'
action "local_command" "run" {
  config {
    command   = "true"
  }
}

resource "terraform_data" "t" {
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.local_command.run]
    }
  }
}
EOF
tf init
tf plan -out=p
save action p

# Guards from a real provider: turned on, left on beside another change, turned off, unknown until apply,
# and deletion_protection, whose off value is false. Fake credentials and -refresh=false keep the provider
# from calling AWS.
workspace guards
cat >main.tf <<'EOF'
terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "6.72.0" }
  }
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "fixture"
  secret_key                  = "fixture"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
  skip_region_validation      = true
}

resource "terraform_data" "later" {
  input = true
}

resource "aws_s3_bucket" "weakened" {
  bucket        = "example-weakened"
  force_destroy = true
}

resource "aws_s3_bucket" "kept" {
  bucket        = "example-kept"
  force_destroy = true
  tags          = { a = "2" }
}

resource "aws_s3_bucket" "guarded" {
  bucket        = "example-guarded"
  force_destroy = false
}

resource "aws_s3_bucket" "unknown" {
  bucket        = "example-unknown"
  force_destroy = terraform_data.later.output
}

resource "aws_db_instance" "unprotected" {
  identifier          = "example-db"
  engine              = "postgres"
  instance_class      = "db.t3.micro"
  allocated_storage   = 20
  username            = "fixture"
  deletion_protection = false
  skip_final_snapshot = false
}
EOF
tf init
bucket() {
  printf '{"mode":"managed","type":"aws_s3_bucket","name":"%s","provider":"provider[\\"registry.terraform.io/hashicorp/aws\\"]","instances":[{"schema_version":0,"attributes":{"id":"example-%s","bucket":"example-%s","force_destroy":%s,"region":"us-east-1","tags":%s,"tags_all":%s},"sensitive_attributes":[]}]}' \
    "$1" "$1" "$1" "$2" "$3" "$3"
}
db='{"mode":"managed","type":"aws_db_instance","name":"unprotected","provider":"provider[\"registry.terraform.io/hashicorp/aws\"]","instances":[{"schema_version":2,"attributes":{"id":"db-FIXTURE","identifier":"example-db","engine":"postgres","instance_class":"db.t3.micro","allocated_storage":20,"username":"fixture","deletion_protection":true,"skip_final_snapshot":false,"region":"us-east-1"},"sensitive_attributes":[]}]}'
printf '{"version":4,"terraform_version":"1.0.0","serial":1,"lineage":"fixture","outputs":{},"resources":[%s,%s,%s,%s,%s]}' \
  "$(bucket weakened false null)" "$(bucket kept true '{"a":"1"}')" \
  "$(bucket guarded true null)" "$(bucket unknown false null)" "$db" >state.json
tf state push state.json
tf plan -refresh=false -out=p
save guards p

cp "$stage"/* "$out"/
