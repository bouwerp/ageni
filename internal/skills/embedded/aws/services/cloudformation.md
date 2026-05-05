# CloudFormation

```bash
# List stacks
aws cloudformation list-stacks \
  --stack-status-filter CREATE_COMPLETE UPDATE_COMPLETE \
  --query 'StackSummaries[].{Name:StackName,Status:StackStatus}'

# Stack details and outputs
aws cloudformation describe-stacks --stack-name NAME
aws cloudformation describe-stacks --stack-name NAME \
  --query 'Stacks[0].Outputs[].[OutputKey,OutputValue]' --output table

# Recent events (for debugging)
aws cloudformation describe-stack-events --stack-name NAME \
  --query 'StackEvents[:10].[Timestamp,ResourceStatus,ResourceType,LogicalResourceId,ResourceStatusReason]' \
  --output table

# Validate template
aws cloudformation validate-template --template-body file://template.yaml

# SAFE deploy: always use change sets
aws cloudformation create-change-set \
  --stack-name NAME --template-body file://template.yaml \
  --change-set-name preview --capabilities CAPABILITY_IAM
aws cloudformation describe-change-set --change-set-name preview --stack-name NAME
# Review the change set, then:
aws cloudformation execute-change-set --change-set-name preview --stack-name NAME
aws cloudformation wait stack-update-complete --stack-name NAME

# Or use deploy (creates + executes change set in one step)
aws cloudformation deploy --template-file template.yaml --stack-name NAME \
  --capabilities CAPABILITY_IAM --no-fail-on-empty-changeset

# Drift detection
DRIFT_ID=$(aws cloudformation detect-stack-drift --stack-name NAME --query 'StackDriftDetectionId' --output text)
aws cloudformation describe-stack-drift-detection-status --stack-drift-detection-id $DRIFT_ID
aws cloudformation describe-stack-resource-drifts --stack-name NAME \
  --stack-resource-drift-status-filters MODIFIED DELETED
```

**Dangerous — require confirmation:**
- `aws cloudformation delete-stack` — deletes stack AND all its resources (unless DeletionPolicy: Retain)
- `aws cloudformation update-stack` with wrong template — can replace/delete resources

**Best practice:** Always use change sets instead of direct `update-stack`. Use `--no-fail-on-empty-changeset` in CI/CD.
