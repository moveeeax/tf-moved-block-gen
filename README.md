# tf-moved-block-gen
Generate Terraform moved blocks from a plan that wants to destroy and recreate resources after a refactor: pair destroys with creates by type and attribute similarity, emit ready-to-paste moved blocks, and fail CI on unexplained destroys.
