# CloudWatch (Logs & Metrics)

```bash
# Tail logs live
aws logs tail /aws/lambda/NAME --follow --since 30m
aws logs tail /aws/lambda/NAME --follow --filter-pattern "ERROR"

# Search historical logs
aws logs filter-log-events \
  --log-group-name /aws/lambda/NAME \
  --start-time $(date -d '1 hour ago' +%s)000 \
  --filter-pattern "Exception"

# CloudWatch Logs Insights query
QUERY_ID=$(aws logs start-query \
  --log-group-name /aws/lambda/NAME \
  --start-time $(date -d '1 hour ago' +%s) \
  --end-time $(date +%s) \
  --query-string 'fields @timestamp, @message | filter @message like /ERROR/ | sort @timestamp desc | limit 50' \
  --query 'queryId' --output text)
aws logs get-query-results --query-id $QUERY_ID

# Metrics
aws cloudwatch get-metric-statistics \
  --namespace AWS/Lambda --metric-name Errors \
  --dimensions Name=FunctionName,Value=NAME \
  --start-time 2024-01-01T00:00:00Z --end-time 2024-01-02T00:00:00Z \
  --period 3600 --statistics Sum

# Active alarms
aws cloudwatch describe-alarms --state-value ALARM \
  --query 'MetricAlarms[].{Name:AlarmName,Reason:StateReason}'
```

**Gotcha:** `filter-log-events` uses `--start-time` in **milliseconds** since epoch. `start-query` uses **seconds**. Always check which one a command expects.
