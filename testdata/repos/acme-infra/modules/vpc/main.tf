terraform {
  required_version = ">= 1.5"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0, < 6.0"
    }
  }
}

variable "cidr" {
  type        = string
  description = "CIDR block for the VPC"
}

variable "tags" {
  default = {}
}

variable "enable_flow_logs" {
  type    = bool
  default = true
}

resource "aws_vpc" "this" {
  cidr_block = var.cidr
  tags       = var.tags
}

resource "aws_subnet" "private" {
  for_each   = toset(["a", "b", "c"])
  vpc_id     = aws_vpc.this.id
  cidr_block = cidrsubnet(var.cidr, 4, index(["a", "b", "c"], each.key))
}

output "id" {
  description = "VPC ID"
  value       = aws_vpc.this.id
}

output "subnet_ids" {
  value = [for s in aws_subnet.private : s.id]
}
