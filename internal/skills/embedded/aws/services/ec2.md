# EC2

```bash
# Describe instances (filtered, formatted)
aws ec2 describe-instances \
  --filters "Name=instance-state-name,Values=running" \
  --query 'Reservations[].Instances[].{ID:InstanceId,Name:Tags[?Key==`Name`].Value|[0],Type:InstanceType,State:State.Name,IP:PrivateIpAddress}' \
  --output table

# Describe a specific instance
aws ec2 describe-instances --instance-ids i-xxx

# Security groups
aws ec2 describe-security-groups --group-ids sg-xxx
aws ec2 describe-security-group-rules --filter "Name=group-id,Values=sg-xxx"

# Latest AMI (self-owned)
aws ec2 describe-images --owners self \
  --query 'reverse(sort_by(Images,&CreationDate))[0].{ID:ImageId,Name:Name,Date:CreationDate}'

# Start/Stop (safe, reversible)
aws ec2 start-instances --instance-ids i-xxx
aws ec2 stop-instances --instance-ids i-xxx

# Dry-run a launch (validates permissions and params, creates nothing)
aws ec2 run-instances --dry-run --image-id ami-xxx --instance-type t3.micro --count 1
```

**Dangerous — require confirmation:**
- `aws ec2 terminate-instances` — permanently destroys instances and root volumes
- `aws ec2 delete-security-group` — can break connectivity
- `aws ec2 delete-snapshot` / `aws ec2 deregister-image` — data loss

**Gotcha:** `--dry-run` returns `DryRunOperation` on success (meaning "would have succeeded"). It only checks permissions, not whether AMI/subnet/SG actually exist.
