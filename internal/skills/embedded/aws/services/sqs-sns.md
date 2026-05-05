# SQS / SNS

```bash
# SQS
aws sqs list-queues
aws sqs get-queue-attributes --queue-url URL --attribute-names All
aws sqs send-message --queue-url URL --message-body '{"event":"test"}'
aws sqs receive-message --queue-url URL --max-number-of-messages 10 --wait-time-seconds 20
aws sqs delete-message --queue-url URL --receipt-handle HANDLE

# SNS
aws sns list-topics
aws sns list-subscriptions-by-topic --topic-arn ARN
aws sns publish --topic-arn ARN --subject "Alert" --message "Something happened"
```

**Dangerous — require confirmation:**
- `aws sqs purge-queue` — deletes ALL messages (can only run once per 60s)
- `aws sqs delete-queue` / `aws sns delete-topic` — permanent deletion

**Gotcha:** `receive-message` may return fewer messages than `--max-number-of-messages`. Default visibility timeout is 30s — unacknowledged messages reappear.
