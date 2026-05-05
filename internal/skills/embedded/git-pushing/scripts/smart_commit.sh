#!/bin/bash

# Smart Commit Script for Git Workflow
# Handles staging, conventional commit message, and pushing with proper setup

set -euo pipefail

# Default commit message if none provided
DEFAULT_MSG="feat: update codebase"

# Use provided message or default
COMMIT_MSG="${1:-$DEFAULT_MSG}"

# Ensure we're in a git repository
if ! git rev-parse --git-dir > /dev/null 2>&1; then
    echo "Error: Not a git repository"
    exit 1
fi

# Check for uncommitted changes
if ! git diff-index --quiet HEAD --; then
    echo "Staging all changes..."
    git add -A
    
    echo "Creating commit: $COMMIT_MSG"
    git commit -m "$COMMIT_MSG"
    
    echo "Pushing to remote..."
    # Get current branch name
    BRANCH=$(git rev-parse --abbrev-ref HEAD)
    
    # Push with -u flag to set upstream if not already set
    git push -u origin "$BRANCH"
    
    echo "✓ Changes committed and pushed successfully!"
else
    echo "No changes to commit"
fi