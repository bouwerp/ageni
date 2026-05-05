#!/usr/bin/env python3
import os
import subprocess
import sys
import argparse
import re
import json

def load_env_file(file_path):
    """Loads secrets from a .env file into a dictionary."""
    secrets = {}
    if not os.path.exists(file_path):
        return secrets
    
    with open(file_path, 'r') as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith('#'):
                continue
            if '=' in line:
                key, value = line.split('=', 1)
                # Remove quotes if present
                value = value.strip().strip('"').strip("'")
                secrets[key.strip()] = value
    return secrets

def redact(text, secrets_values):
    """Redacts known secret values from text."""
    if not secrets_values:
        return text
    
    # Sort by length descending to avoid partial redaction of longer secrets
    sorted_secrets = sorted([s for s in secrets_values if s and len(s) > 3], key=len, reverse=True)
    
    redacted_text = text
    for secret in sorted_secrets:
        # Simple string replacement (case-sensitive)
        redacted_text = redacted_text.replace(secret, "[REDACTED]")
    
    return redacted_text

def main():
    parser = argparse.ArgumentParser(description="Run a command with secrets and redact them from output.")
    parser.add_argument("--source", default=".env", help="Path to a .env or secrets file (default: .env)")
    parser.add_argument("--secrets-json", help="Path to a JSON file containing secrets (e.g., connections.json style)")
    parser.add_argument("--command", required=True, help="The command to execute")
    parser.add_argument("--no-redact", action="store_true", help="Disable redaction (not recommended)")
    
    args, unknown = parser.parse_known_args()
    
    # 1. Gather secrets
    secrets = {}
    if os.path.exists(args.source):
        secrets.update(load_env_file(args.source))
    
    if args.secrets_json and os.path.exists(args.secrets_json):
        try:
            with open(args.secrets_json, 'r') as f:
                data = json.load(f)
                # Flatten or extract based on structure (assuming flat for now)
                if isinstance(data, dict):
                    secrets.update(data)
        except Exception as e:
            print(f"Error loading secrets-json: {e}", file=sys.stderr)

    # Filter out common/short values that would cause false positives in redaction
    # (e.g., "true", "1", "production")
    exclude_list = {'true', 'false', 'none', 'null', 'yes', 'no', '1', '0'}
    secrets_to_redact = [v for v in secrets.values() if v.lower() not in exclude_list and len(v) > 3]

    # 2. Setup environment
    env = os.environ.copy()
    env.update(secrets)

    # 3. Execute command
    try:
        # Run process and capture output
        process = subprocess.Popen(
            args.command,
            shell=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT, # Merge stderr into stdout
            env=env,
            text=True,
            bufsize=1, # Line-buffered
            universal_newlines=True
        )

        print(f"Executing command with secrets from {args.source}...")
        
        while True:
            line = process.stdout.readline()
            if not line and process.poll() is not None:
                break
            if line:
                if not args.no_redact:
                    line = redact(line, secrets_to_redact)
                sys.stdout.write(line)
                sys.stdout.flush()

        process.wait()
        
        if process.returncode != 0:
            print(f"\nCommand failed with exit code {process.returncode}", file=sys.stderr)
            sys.exit(process.returncode)

    except Exception as e:
        print(f"Error executing command: {e}", file=sys.stderr)
        sys.exit(1)

if __name__ == "__main__":
    main()
