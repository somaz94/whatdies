# Planned offline: capture.sh pushes a state where the bucket exists with the
# guard on, and plans with fake credentials and -refresh=false, so AWS is never called.
terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "6.72.0" }
  }
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "demo"
  secret_key                  = "demo"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
  skip_region_validation      = true
}

resource "aws_s3_bucket" "invoices" {
  bucket        = "example-invoices"
  force_destroy = true
}
