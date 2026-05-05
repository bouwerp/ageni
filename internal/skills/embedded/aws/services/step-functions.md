# Step Functions

```bash
# List state machines
aws stepfunctions list-state-machines --query 'stateMachines[].{Name:name,Arn:stateMachineArn}'

# Start execution
aws stepfunctions start-execution --state-machine-arn ARN --input '{"key":"value"}'

# Check execution status
aws stepfunctions describe-execution --execution-arn EXEC_ARN

# Event history (latest first)
aws stepfunctions get-execution-history --execution-arn EXEC_ARN --reverse-order

# List running executions
aws stepfunctions list-executions --state-machine-arn ARN --status-filter RUNNING
```

**Gotcha:** `start-execution` returns immediately. Poll `describe-execution` for results. Execution names must be unique per state machine for 90 days.
