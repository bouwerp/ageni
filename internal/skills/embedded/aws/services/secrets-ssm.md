# Secrets Manager / SSM Parameter Store

```bash
# Secrets Manager
aws secretsmanager list-secrets --query 'SecretList[].{Name:Name,Changed:LastChangedDate}'
aws secretsmanager get-secret-value --secret-id NAME --query 'SecretString' --output text

# SSM Parameter Store
aws ssm get-parameter --name /app/config/key --with-decryption --query 'Parameter.Value' --output text
aws ssm get-parameters-by-path --path /app/config/ --recursive --with-decryption

# Access Secrets Manager through Parameter Store
aws ssm get-parameter --name /aws/reference/secretsmanager/SECRET_NAME --with-decryption
```

**Dangerous — require confirmation:**
- `aws secretsmanager delete-secret --force-delete-without-recovery` — permanent, no recovery
- `aws ssm delete-parameter` — immediate, no recovery

**Security:** Secrets passed as CLI arguments are visible in shell history and `ps` output. For sensitive values, use `--cli-input-json file://input.json` instead.

**Gotcha:** `--with-decryption` is required for SecureString parameters — without it you get the encrypted blob.
