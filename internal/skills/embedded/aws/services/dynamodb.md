# DynamoDB

```bash
# List and describe tables
aws dynamodb list-tables
aws dynamodb describe-table --table-name TABLE

# Get single item
aws dynamodb get-item --table-name TABLE \
  --key '{"pk":{"S":"value"}}' --consistent-read

# Query (efficient, uses index)
aws dynamodb query --table-name TABLE \
  --key-condition-expression "pk = :pk" \
  --expression-attribute-values '{":pk":{"S":"value"}}' \
  --limit 10

# Scan (expensive — always limit)
aws dynamodb scan --table-name TABLE --max-items 10

# Conditional put (prevents overwrite)
aws dynamodb put-item --table-name TABLE \
  --item '{"pk":{"S":"123"},"name":{"S":"test"}}' \
  --condition-expression "attribute_not_exists(pk)"

# Monitor capacity consumption
aws dynamodb query ... --return-consumed-capacity TOTAL
```

**Dangerous — require confirmation:**
- `aws dynamodb delete-table` — permanently removes table and ALL data
- `aws dynamodb put-item` without `--condition-expression` — silently overwrites existing items
- `aws dynamodb scan` on large tables — consumes massive RCUs, can throttle the table

**Gotcha:** `--limit` (DynamoDB API parameter) and `--max-items` (CLI parameter) behave differently. Use `--max-items` to control total output. Filter expressions do NOT reduce RCU consumption.
