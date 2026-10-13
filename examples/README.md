# examples

`plan.json` is a trimmed `terraform show -json tfplan` document that carries one
of everything the tool has to tell apart:

| resource | plan action | what the tool should do |
| --- | --- | --- |
| `aws_instance.web` → `aws_instance.api` | delete + create | pair them, confidence 0.85 |
| `module.app.aws_security_group.lb` → `module.edge...lb` | delete + create | pair them, confidence 1.00 |
| `aws_s3_bucket.legacy_logs` | delete | report as an unexplained destroy |
| `aws_launch_template.workers` | delete + create of the same address | report as a replacement, never a move |
| `aws_vpc.main` | no-op | ignore |
| `aws_iam_role.app` | update | ignore |
| `data.aws_ami.ubuntu` | read | ignore (data sources have no state to move) |

`moved.tf` is the committed output of:

```console
tf-moved-block-gen --plan examples/plan.json > examples/moved.tf
```

CI diffs the two, so any change in the pairing or the rendering has to be
committed deliberately.
