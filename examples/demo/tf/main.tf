# terraform_data stands in for a database: no cloud account, no provider download.
# triggers_replace forces a replacement when engine changes, which is what an
# engine upgrade does to a real database resource.
variable "engine" {
  type    = string
  default = "15"
}

variable "note" {
  type    = string
  default = "first"
}

resource "terraform_data" "customer_db" {
  triggers_replace = var.engine
  input            = var.note
}
