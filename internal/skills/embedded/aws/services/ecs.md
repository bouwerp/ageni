# ECS / Fargate

```bash
# List clusters and services
aws ecs list-clusters
aws ecs list-services --cluster CLUSTER

# Service status
aws ecs describe-services --cluster CLUSTER --services SVC \
  --query 'services[0].{Status:status,Desired:desiredCount,Running:runningCount,TaskDef:taskDefinition}'

# Task details
aws ecs list-tasks --cluster CLUSTER --service-name SVC
aws ecs describe-tasks --cluster CLUSTER --tasks TASK_ARN

# Force new deployment (same task def)
aws ecs update-service --cluster CLUSTER --service SVC --force-new-deployment
aws ecs wait services-stable --cluster CLUSTER --services SVC

# Scale
aws ecs update-service --cluster CLUSTER --service SVC --desired-count N

# Exec into container (requires SSM Session Manager plugin)
aws ecs execute-command --cluster CLUSTER --task TASK_ID \
  --container CONTAINER --interactive --command "/bin/sh"
```

**Dangerous — require confirmation:**
- `aws ecs delete-service --force` — deletes service with running tasks
- `aws ecs delete-cluster` — removes the cluster
- `aws ecs update-service --desired-count 0` — scales to zero

**Gotcha:** ECS Exec requires the task role (not execution role) to have `ssmmessages:*` permissions, and the service must have `--enable-execute-command` set.
