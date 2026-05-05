# IAM

IAM is **read-only for agents.** Never create, delete, or modify IAM resources via CLI — use Infrastructure as Code (CloudFormation, CDK, Terraform).

```bash
# Who am I?
aws sts get-caller-identity

# Audit roles and policies
aws iam list-roles --query 'Roles[].{Name:RoleName,Arn:Arn}' --output table
aws iam get-role --role-name NAME
aws iam list-attached-role-policies --role-name NAME
aws iam get-policy-version --policy-arn ARN --version-id v1

# Audit users and access keys
aws iam list-users --query 'Users[].{Name:UserName,Created:CreateDate}'
aws iam list-access-keys --user-name NAME

# Test permissions without executing
aws iam simulate-principal-policy \
  --policy-source-arn ROLE_ARN \
  --action-names s3:GetObject \
  --resource-arns "arn:aws:s3:::bucket/*"

# Credential report
aws iam generate-credential-report
aws iam get-credential-report --query 'Content' --output text | base64 --decode
```

**Never run via CLI:** `create-user`, `delete-user`, `create-role`, `delete-role`, `attach-*-policy`, `detach-*-policy`, `put-*-policy`, `create-access-key`. These must be managed through IaC.
