#!/usr/bin/env python3
import os
import re
import sys
import argparse

# Common secret patterns
PATTERNS = {
    "Generic Secret/Token": re.compile(r'(?:secret|token|key|pwd|password|auth|api)[-_]?(?:key|token|secret)?\s*[:=]\s*["\']?([a-zA-Z0-9\-_.~]{16,})["\']?', re.IGNORECASE),
    "AWS Access Key": re.compile(r'AKIA[0-9A-Z]{16}'),
    "AWS Secret Access Key": re.compile(r'[^a-zA-Z0-9/+=]([a-zA-Z0-9/+=]{40})[^a-zA-Z0-9/+=]'),
    "GitHub Personal Access Token": re.compile(r'ghp_[a-zA-Z0-9]{36}'),
    "Slack Webhook": re.compile(r'https://hooks.slack.com/services/T[a-zA-Z0-9_]{8}/B[a-zA-Z0-9_]{8}/[a-zA-Z0-9_]{24}'),
    "Google API Key": re.compile(r'AIza[0-9A-Za-z-_]{35}'),
    "Private Key": re.compile(r'-----BEGIN [A-Z ]+ PRIVATE KEY-----'),
}

def scan_file(file_path):
    """Scans a single file for secrets."""
    findings = []
    if not os.path.exists(file_path):
        return findings

    try:
        with open(file_path, 'r', errors='ignore') as f:
            for line_num, line in enumerate(f, 1):
                for name, pattern in PATTERNS.items():
                    matches = pattern.findall(line)
                    if matches:
                        findings.append({
                            "type": name,
                            "line": line_num,
                            "context": line.strip()[:50] + "..." # Truncate for safety
                        })
    except Exception as e:
        print(f"Error reading {file_path}: {e}", file=sys.stderr)
    
    return findings

def main():
    parser = argparse.ArgumentParser(description="Scan files for potential secrets without displaying values.")
    parser.add_argument("path", help="File or directory to scan")
    parser.add_argument("--exclude", help="Regex of files to exclude", default=r'node_modules|\.git')
    
    args = parser.parse_args()
    exclude_re = re.compile(args.exclude)
    
    all_findings = {}
    
    if os.path.isfile(args.path):
        f = scan_file(args.path)
        if f: all_findings[args.path] = f
    else:
        for root, dirs, files in os.walk(args.path):
            dirs[:] = [d for d in dirs if not exclude_re.search(d)]
            for file in files:
                if exclude_re.search(file):
                    continue
                file_path = os.path.join(root, file)
                f = scan_file(file_path)
                if f:
                    all_findings[file_path] = f
                    
    if not all_findings:
        print("No obvious secrets found.")
    else:
        print("Potential secrets detected:")
        for file, findings in all_findings.items():
            print(f"\nFile: {file}")
            for item in findings:
                # Redact context even more just in case
                safe_context = item['context']
                for name, pattern in PATTERNS.items():
                    safe_context = pattern.sub("[REDACTED]", safe_context)
                print(f"  Line {item['line']}: {item['type']} -> {safe_context}")

if __name__ == "__main__":
    main()
