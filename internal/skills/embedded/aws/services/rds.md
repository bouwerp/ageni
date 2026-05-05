# RDS

```bash
# List instances
aws rds describe-db-instances \
  --query 'DBInstances[].{ID:DBInstanceIdentifier,Status:DBInstanceStatus,Engine:Engine,Class:DBInstanceClass}' \
  --output table

# Instance details
aws rds describe-db-instances --db-instance-identifier NAME

# List snapshots
aws rds describe-db-snapshots --db-instance-identifier NAME \
  --query 'DBSnapshots[].{ID:DBSnapshotIdentifier,Created:SnapshotCreateTime,Status:Status}'

# Create snapshot (safe, non-destructive)
aws rds create-db-snapshot \
  --db-instance-identifier NAME \
  --db-snapshot-identifier snap-$(date +%Y%m%d-%H%M%S)

# Download error log
aws rds download-db-log-file-portion --db-instance-identifier NAME \
  --log-file-name error/mysql-error.log
```

**Dangerous — require confirmation:**
- `aws rds delete-db-instance` — especially with `--skip-final-snapshot`
- `aws rds delete-db-snapshot` — permanent data loss
- `aws rds modify-db-instance --publicly-accessible` — exposes DB to internet

**Gotcha:** `stop-db-instance` auto-restarts after 7 days. Creating a snapshot on Single-AZ causes brief I/O suspension.
