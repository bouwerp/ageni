# Route53

```bash
# List hosted zones
aws route53 list-hosted-zones --query 'HostedZones[].{Name:Name,ID:Id}'

# List records
aws route53 list-resource-record-sets --hosted-zone-id ZONE_ID \
  --query 'ResourceRecordSets[].{Name:Name,Type:Type,Value:ResourceRecords[0].Value}' --output table

# Upsert a record (creates if missing, updates if exists)
aws route53 change-resource-record-sets --hosted-zone-id ZONE_ID \
  --change-batch file://change.json

# Wait for propagation
aws route53 wait resource-record-sets-changed --id /change/CHANGE_ID
```

**Dangerous — require confirmation:**
- `Action: "DELETE"` in change-batch — removes DNS records
- `aws route53 delete-hosted-zone` — removes entire zone

**Gotcha:** `change-resource-record-sets` requires JSON input via `file://`. DNS propagation can take up to 60s after API success.
