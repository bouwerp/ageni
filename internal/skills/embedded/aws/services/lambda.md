# Lambda

```bash
# List functions
aws lambda list-functions \
  --query 'Functions[].{Name:FunctionName,Runtime:Runtime,Memory:MemorySize}' --output table

# Get function details
aws lambda get-function --function-name NAME
aws lambda get-function-configuration --function-name NAME

# Tail logs (live)
aws logs tail /aws/lambda/NAME --follow --since 30m

# Invoke (synchronous)
aws lambda invoke --function-name NAME --payload '{"key":"value"}' output.json --log-type Tail

# Decode invocation logs
aws lambda invoke --function-name NAME out --log-type Tail \
  --query 'LogResult' --output text | base64 --decode

# Permission check only (no execution)
aws lambda invoke --function-name NAME --invocation-type DryRun output.json

# Safe deployment pipeline
aws lambda update-function-code --function-name NAME --zip-file fileb://code.zip
aws lambda wait function-updated --function-name NAME
aws lambda publish-version --function-name NAME
```

**Dangerous — require confirmation:**
- `aws lambda delete-function` — permanent deletion
- `aws lambda put-function-concurrency --reserved-concurrent-executions 0` — effectively disables the function

**Gotcha:** After `update-function-code`, VPC-attached functions can take ~60s to become ready. Always use `aws lambda wait function-updated`.
