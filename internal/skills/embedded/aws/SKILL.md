---
name: aws
description: This skill should be used when the user asks to interact with AWS services via the CLI — listing resources, checking logs, deploying infrastructure, managing S3, EC2, Lambda, ECS, CloudFormation, IAM, CloudWatch, RDS, DynamoDB, SQS, SNS, Secrets Manager, SSM, ECR, Route53, API Gateway, Step Functions, or Cost Explorer. Provides safe AWS CLI patterns with a read-first, confirm-before-mutating approach. Per-service command reference lives in services/<name>.md — read the relevant one on demand.
version: 2.0.0
---

# AWS CLI Interaction

**Portability:** Any agent with a **shell** can use these patterns. Credentials and profiles are whatever the user configured (`aws configure`, SSO, environment variables).

Safe, structured patterns for interacting with AWS services via the `aws` CLI. This skill enforces a **read-first, confirm-before-mutating** approach to prevent accidental resource destruction and cost surprises.

## How to use this skill

1. Apply the **Core principles** and **Command safety classification** below to every command.
2. Before any session, run the **Identity check** (`aws sts get-caller-identity` + region).
3. For a specific service, read the matching file under `services/`:

| Service | File |
|---|---|
| S3 | [services/s3.md](services/s3.md) |
| EC2 | [services/ec2.md](services/ec2.md) |
| Lambda | [services/lambda.md](services/lambda.md) |
| ECS / Fargate | [services/ecs.md](services/ecs.md) |
| CloudFormation | [services/cloudformation.md](services/cloudformation.md) |
| IAM (read-only) | [services/iam.md](services/iam.md) |
| CloudWatch (Logs & Metrics) | [services/cloudwatch.md](services/cloudwatch.md) |
| RDS | [services/rds.md](services/rds.md) |
| DynamoDB | [services/dynamodb.md](services/dynamodb.md) |
| SQS / SNS | [services/sqs-sns.md](services/sqs-sns.md) |
| Secrets Manager / SSM | [services/secrets-ssm.md](services/secrets-ssm.md) |
| ECR | [services/ecr.md](services/ecr.md) |
| Route53 | [services/route53.md](services/route53.md) |
| Step Functions | [services/step-functions.md](services/step-functions.md) |
| Cost Explorer | [services/cost-explorer.md](services/cost-explorer.md) |

Each service file follows the same shape: common commands, **Dangerous — require confirmation** list, and a **Gotcha** call-out.

## Core principles

1. **Identity first.** Always verify who you are before doing anything.
2. **Read before write.** Use `describe-*` / `list-*` / `get-*` before any mutation.
3. **Dry-run when available.** Use `--dry-run` (EC2) and `--dryrun` (S3) to preview.
4. **Confirm before mutating.** Never auto-run create, update, or delete commands — present the plan and wait.
5. **Never auto-delete.** Terminate, delete, and remove commands require explicit user approval every time.
6. **Tag everything.** All created resources get metadata tags for tracking.
7. **Limit output.** Use `--max-items`, `--query`, and `--output json` to keep responses manageable.

## Identity check (run first, every session)

```bash
aws sts get-caller-identity       # account, role, identity
aws configure get region          # target region
```

This prevents operating on the wrong account or region.

## Command safety classification

| Category | Prefixes | Agent behaviour |
|----------|----------|-----------------|
| **Safe (read-only)** | `describe-*`, `list-*`, `get-*`, `head-*`, `wait` | Run freely |
| **Mostly safe (creates)** | `create-*`, `put-*`, `tag-*`, `start-*` | Show user what will be created, warn about costs |
| **Dangerous (mutates)** | `update-*`, `modify-*`, `stop-*` | Describe current state first, confirm before running |
| **Very dangerous (destroys)** | `delete-*`, `terminate-*`, `remove-*`, `deregister-*`, `purge-*` | ALWAYS require explicit user confirmation |

## Global flags

Use these on every command for predictable, non-blocking output:

```bash
aws <service> <command> \
  --output json \
  --no-cli-pager \
  --region <region>
```

| Flag | Purpose |
|------|---------|
| `--output json` | Machine-parseable output |
| `--no-cli-pager` | Prevents interactive pager from blocking execution |
| `--region us-east-1` | Explicit region — never rely on defaults in automation |
| `--query '<jmespath>'` | Filter output to only needed fields |
| `--max-items N` | Limit pagination to N results |
| `--dry-run` | Preview EC2 operations without executing |

## JMESPath patterns

Use `--query` to extract only what you need:

```bash
# Select specific fields into a table
--query 'Resources[].{Name:Name, ID:Id, Status:Status}' --output table

# Filter by value
--query 'Items[?State==`active`]'

# Sort / latest / count
--query 'sort_by(Items, &CreatedAt)'
--query 'reverse(sort_by(Items, &CreatedAt))[0]'
--query 'length(Items)'

# Null-safe tag extraction
--query 'Instances[].{ID:InstanceId, Name:Tags[?Key==`Name`].Value|[0]}'
```

For transformations beyond JMESPath, pipe to `jq`:

```bash
aws ec2 describe-instances --output json | jq -r \
  '.Reservations[].Instances[] | select(.State.Name=="running") | .InstanceId'
```

## Pagination

```bash
--no-paginate         # first page only (quick sample)
--max-items 20        # limit total across pages
--page-size 50        # items per API call (reduces timeout risk)
--starting-token <T>  # resume from previous position
```

**Important:** `--no-paginate` means "first page only", not "all results at once." For agents: always set `--max-items` to keep output within context limits.

## Waiters

Use waiters instead of sleep loops:

```bash
aws ec2 wait instance-running --instance-ids i-xxx
aws ecs wait services-stable --cluster C --services S
aws rds wait db-instance-available --db-instance-identifier DB
aws cloudformation wait stack-create-complete --stack-name N
aws lambda wait function-updated --function-name F
```

Waiters poll at regular intervals and exit when the condition is met or timeout is reached (typically ~6 minutes).

## Error handling

| Exit code | Meaning |
|-----------|---------|
| 0 | Success |
| 1 | S3 transfer failure |
| 2 | Parse error |
| 252 | Invalid syntax/parameters |
| 253 | Invalid configuration/credentials |
| 254 | AWS service returned an error |

Use `--debug` to get full HTTP request/response details when troubleshooting.

## Credential management

```bash
# Current identity
aws sts get-caller-identity

# Named profile
aws s3 ls --profile production
export AWS_PROFILE=production

# SSO login
aws sso login --profile my-sso-profile

# Assume role → temporary credentials
CREDS=$(aws sts assume-role --role-arn arn:aws:iam::123456789012:role/MyRole --role-session-name agent-session)
export AWS_ACCESS_KEY_ID=$(echo $CREDS | jq -r '.Credentials.AccessKeyId')
export AWS_SECRET_ACCESS_KEY=$(echo $CREDS | jq -r '.Credentials.SecretAccessKey')
export AWS_SESSION_TOKEN=$(echo $CREDS | jq -r '.Credentials.SessionToken')
```

Prefer SSO or `assume-role` over long-lived access keys. Never commit credentials. Use `--profile` explicitly rather than relying on environment defaults. Role chaining limits sessions to 1 hour max.

## Cost awareness

Before creating any resource, consider:

1. **Instance type cost** — Verify type/class is appropriate (avoid accidentally using p4d/p5 GPU instances).
2. **Region** — Prices vary significantly; confirm target region.
3. **Data transfer** — S3 sync, cross-region copies, and NAT Gateway traffic incur costs.
4. **Always-on resources** — NAT Gateways, RDS instances, ECS services, and Elasticsearch domains charge per hour.
5. **DynamoDB scans** — Full table scans consume read capacity proportional to table size.

See [services/cost-explorer.md](services/cost-explorer.md) for current-spend queries.

## IaC alignment (CDK, serverless)

- **Prefer generated physical names** where optional — avoids collisions and aids parallel stacks. Use account/OU separation for environments, not clever shared fixed names.
- **Validate before deploy:** `cdk synth`; add synthesis-time checks (e.g. cdk-nag / AwsSolutions) where the project uses them.
- **Deploy via change sets / pipelines** for production; never bypass review for shared environments. See [services/cloudformation.md](services/cloudformation.md).
- **Serverless:** small single-purpose functions; design for concurrency and downstream throttling, not total requests. Prefer Step Functions or event buses over long synchronous Lambda chains. Treat local disk as ephemeral.
- **Idempotency:** SQS/EventBridge/Lambda retries mean handlers must tolerate duplicates; use idempotency keys and conditional writes.
- **Failure paths:** Dead-letter queues, partial batch responses for SQS, alarms on DLQ depth.

## Discovering command parameters

Use `--generate-cli-skeleton` to see every parameter a command accepts:

```bash
aws ec2 run-instances --generate-cli-skeleton input
```

Fill in the resulting JSON and pass it back via `--cli-input-json file://input.json`.
