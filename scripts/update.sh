#!/bin/bash
set -e

# ageni update script.
# Updates an installed ageni binary to the latest release. The recommended
# in-binary path is `ageni update`; this script is for environments where the
# binary cannot self-replace (e.g. system-wide installs running under another
# user) or for scripted updates.

REPO_URL="https://github.com/bouwerp/ageni"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
BINARY_NAME="ageni"
BACKUP_SUFFIX=".backup.$(date +%Y%m%d_%H%M%S)"
GITHUB_API="https://api.github.com/repos/bouwerp/ageni/releases/latest"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

CURRENT_VERSION="${CURRENT_VERSION:-unknown}"

find_installation() {
    if command -v ageni &> /dev/null; then
        AGENI_PATH=$(which ageni)
        INSTALL_DIR=$(dirname "$AGENI_PATH")
        echo -e "${BLUE}Found ageni at: $AGENI_PATH${NC}"

        if [ -f "$AGENI_PATH" ]; then
            CURRENT_VERSION=$("$AGENI_PATH" --version 2>/dev/null || echo "unknown")
            echo -e "${BLUE}Current version: $CURRENT_VERSION${NC}"
        fi
    else
        echo -e "${RED}Error: ageni not found in PATH${NC}"
        echo "Please ensure ageni is installed before updating."
        exit 1
    fi
}

get_latest_version() {
    echo "Checking for latest version..."

    LATEST_VERSION=$(curl -s "$GITHUB_API" |
                     grep '"tag_name":' |
                     sed -E 's/.*"([^"]+)".*/\1/' 2>/dev/null || echo "")

    if [ -z "$LATEST_VERSION" ]; then
        echo -e "${YELLOW}Could not fetch latest version info. Will update to latest commit.${NC}"
        LATEST_VERSION="latest"
    else
        echo -e "${GREEN}Latest version: $LATEST_VERSION${NC}"
    fi

    if [ "$CURRENT_VERSION" = "$LATEST_VERSION" ] && [ "$FORCE_UPDATE" != "1" ]; then
        echo -e "${GREEN}Already up to date!${NC}"
        exit 0
    fi
}

backup_current() {
    if [ "$SKIP_BACKUP" = "1" ]; then
        echo -e "${YELLOW}Skipping backup (--skip-backup specified)${NC}"
        return
    fi
    echo "Creating backup of current binary..."
    BACKUP_PATH="${AGENI_PATH}${BACKUP_SUFFIX}"
    cp "$AGENI_PATH" "$BACKUP_PATH"
    echo -e "${GREEN}Backup created: $BACKUP_PATH${NC}"
}

detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)

    case "$ARCH" in
        x86_64) ARCH="amd64" ;;
        arm64|aarch64) ARCH="arm64" ;;
    esac

    case "$OS" in
        darwin)
            RELEASE_NAME="ageni-darwin-${ARCH}"
            ARCHIVE_EXT="tar.gz"
            ;;
        linux)
            RELEASE_NAME="ageni-linux-${ARCH}"
            ARCHIVE_EXT="tar.gz"
            ;;
        windows|mingw*|msys*)
            RELEASE_NAME="ageni-windows-${ARCH}.exe"
            ARCHIVE_EXT="zip"
            ;;
    esac
}

download_binary() {
    echo "Attempting to download pre-built binary..."

    detect_platform

    if ! RELEASE_DATA=$(curl -sf "$GITHUB_API" 2>/dev/null); then
        echo -e "${YELLOW}No release found, will build from source${NC}"
        return 1
    fi

    LATEST_VERSION=$(echo "$RELEASE_DATA" | grep '"tag_name"' | cut -d '"' -f 4)
    DOWNLOAD_URL=$(echo "$RELEASE_DATA" | grep "browser_download_url.*${RELEASE_NAME}.${ARCHIVE_EXT}\"" | cut -d '"' -f 4)

    if [ -z "$DOWNLOAD_URL" ]; then
        echo -e "${YELLOW}Pre-built binary not available for ${OS}-${ARCH}, will build from source${NC}"
        return 1
    fi

    echo -e "${GREEN}Downloading version: $LATEST_VERSION${NC}"

    TEMP_DIR=$(mktemp -d)
    cd "$TEMP_DIR"

    if ! curl -sfL "$DOWNLOAD_URL" -o "${RELEASE_NAME}.${ARCHIVE_EXT}"; then
        echo -e "${RED}Download failed${NC}"
        return 1
    fi

    case "$ARCHIVE_EXT" in
        tar.gz) tar -xzf "${RELEASE_NAME}.${ARCHIVE_EXT}" ;;
        zip)    unzip -q "${RELEASE_NAME}.${ARCHIVE_EXT}" ;;
    esac

    if [ ! -f "$RELEASE_NAME" ]; then
        echo -e "${RED}Binary not found in archive${NC}"
        return 1
    fi

    mv "$RELEASE_NAME" "$BINARY_NAME"
    chmod +x "$BINARY_NAME"

    echo -e "${GREEN}Download successful!${NC}"
    return 0
}

fetch_source() {
    TEMP_DIR=$(mktemp -d)
    cd "$TEMP_DIR"

    echo "Fetching latest source code..."
    git clone --depth 1 "$REPO_URL" ageni-src 2>/dev/null || {
        echo -e "${RED}Failed to clone repository${NC}"
        exit 1
    }

    cd ageni-src
}

build_new_version() {
    echo "Building new version..."

    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)

    case "$ARCH" in
        x86_64) ARCH="amd64" ;;
        arm64|aarch64) ARCH="arm64" ;;
    esac

    export GOOS="$OS"
    export GOARCH="$ARCH"

    VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
    BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

    go build -ldflags "-X main.version=$VERSION -X main.buildTime=$BUILD_TIME" \
        -o "${BINARY_NAME}" \
        ./cmd/ageni

    if [ $? -ne 0 ]; then
        echo -e "${RED}Build failed!${NC}"
        restore_backup
        exit 1
    fi

    echo -e "${GREEN}Build successful!${NC}"
}

install_new_version() {
    echo "Installing new version..."

    if [ -w "$INSTALL_DIR" ]; then
        mv "${BINARY_NAME}" "$AGENI_PATH"
    else
        echo -e "${YELLOW}Requesting sudo access to update $AGENI_PATH${NC}"
        sudo mv "${BINARY_NAME}" "$AGENI_PATH"
    fi

    chmod +x "$AGENI_PATH"

    NEW_VERSION=$("$AGENI_PATH" --version 2>/dev/null || echo "unknown")
    echo -e "${GREEN}Updated to version: $NEW_VERSION${NC}"
}

restore_backup() {
    if [ "$SKIP_BACKUP" = "1" ]; then
        echo -e "${RED}Cannot restore: backup was skipped${NC}"
        return
    fi
    if [ -f "$BACKUP_PATH" ]; then
        echo "Restoring previous version..."
        cp "$BACKUP_PATH" "$AGENI_PATH"
        echo -e "${GREEN}Previous version restored${NC}"
    fi
}

cleanup_old_backups() {
    echo "Cleaning up old backups..."
    find "$INSTALL_DIR" -name "${BINARY_NAME}.backup.*" -type f |
        sort -r |
        tail -n +6 |
        xargs -r rm -f
}

cleanup() {
    if [ -n "$TEMP_DIR" ] && [ -d "$TEMP_DIR" ]; then
        rm -rf "$TEMP_DIR"
    fi
}

trap cleanup EXIT

show_changelog() {
    echo ""
    echo -e "${BLUE}Recent changes:${NC}"
    git log --oneline -10 2>/dev/null || echo "Changelog not available"
    echo ""
}

main() {
    echo "=== ageni update ==="
    echo ""

    find_installation
    get_latest_version
    backup_current

    if download_binary; then
        install_new_version
    else
        echo ""
        echo -e "${YELLOW}Falling back to building from source...${NC}"
        fetch_source
        show_changelog
        build_new_version
        install_new_version
    fi

    cleanup_old_backups

    echo ""
    echo -e "${GREEN}Update complete!${NC}"
    echo ""
    echo "Run 'ageni --version' to verify the update."
    if [ "$SKIP_BACKUP" != "1" ]; then
        echo "If you encounter any issues, you can restore the previous version from:"
        echo "  $BACKUP_PATH"
    fi
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --prefix)
            INSTALL_DIR="$2"
            shift 2
            ;;
        --force)
            FORCE_UPDATE=1
            shift
            ;;
        --skip-backup)
            SKIP_BACKUP=1
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --prefix DIR    Specify installation directory"
            echo "  --force         Force update even if already on latest version"
            echo "  --skip-backup   Skip creating backup (not recommended)"
            echo "  -h, --help      Show this help message"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

main
