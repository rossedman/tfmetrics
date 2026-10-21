terraform {
  cloud {
    organization = "acme"
    workspaces {
      name = "staging"
    }
  }

  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

module "vpc" {
  source = "../../modules/vpc"
  cidr   = "10.1.0.0/16"
}

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "17.24.0"
}

resource "aws_sqs_queue" "jobs" {
  name = "staging-jobs"
}
