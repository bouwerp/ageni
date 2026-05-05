# ECR

```bash
# Authenticate Docker to ECR
aws ecr get-login-password --region REGION | \
  docker login --username AWS --password-stdin ACCOUNT.dkr.ecr.REGION.amazonaws.com

# List repositories
aws ecr describe-repositories --query 'repositories[].{Name:repositoryName,URI:repositoryUri}'

# Recent images
aws ecr describe-images --repository-name REPO \
  --query 'reverse(sort_by(imageDetails,&imagePushedAt))[:5].[imageTags[0],imagePushedAt]'

# Find untagged images
aws ecr list-images --repository-name REPO --filter tagStatus=UNTAGGED
```

**Dangerous — require confirmation:**
- `aws ecr delete-repository --force` — deletes repo AND all images

**Gotcha:** ECR auth tokens expire after 12 hours. Always pipe `get-login-password` directly to `docker login --password-stdin`.
