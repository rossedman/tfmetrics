resource "aws_s3_bucket" "oops" {
  bucket = "missing-brace"
