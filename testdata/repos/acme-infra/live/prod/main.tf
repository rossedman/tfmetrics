terraform {
  required_version = ">= 1.6.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }

  backend "s3" {
    bucket = "acme-tf-state"
    key    = "prod/terraform.tfstate"
    region = "us-east-1"
  }
}

provider "aws" {
  region = "us-east-1"
}

module "vpc" {
  source = "../../modules/vpc"
  cidr   = "10.0.0.0/16"
}

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "19.21.0"
}

module "s3_bucket" {
  source  = "terraform-aws-modules/s3-bucket/aws"
  version = "~> 3.15"
}

module "dns" {
  source = "git::https://github.com/acme/tf-dns.git?ref=v1.4.0"
}

module "iam" {
  source = "github.com/acme/tf-iam"
}

module "queues" {
  source   = "app.terraform.io/acme/sqs/aws"
  version  = "2.1.0"
  for_each = toset(["orders", "billing"])
  name     = each.key
}

resource "aws_s3_bucket" "logs" {
  bucket = "acme-prod-logs"

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_kms_key" "state" {
  description = "state encryption"

  lifecycle {
    prevent_destroy       = true
    create_before_destroy = true
  }
}

resource "aws_instance" "bastion" {
  count         = 2
  ami           = data.aws_ami.al2023.id
  instance_type = "t3.micro"

  lifecycle {
    ignore_changes = [ami]
  }
}

data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]
}

moved {
  from = aws_instance.jump
  to   = aws_instance.bastion
}

import {
  to = aws_s3_bucket.logs
  id = "acme-prod-logs"
}

output "vpc_id" {
  description = "ID of the production VPC"
  value       = module.vpc.id
}
