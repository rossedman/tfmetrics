module "vpc" {
  source = "../../modules/vpc"
  cidr   = "10.9.0.0/16"
}
