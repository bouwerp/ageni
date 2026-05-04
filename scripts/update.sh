#!/bin/bash
set -e

# ageni update script.
# Updates an installed ageni binary to the latest release by downloading the
# platform-specific pre-built binary from GitHub Releases. The recommended
# path is `ageni update` (in-binary self-update); this script is for
# environments where the binary cannot self-replace or for scripted updates.
# Fails if no pre-built binary is available for the detected platform.

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
    echo "Fetching release metadata..."

    detect_platform

    if ! RELEASE_DATA=$(curl -sf "$GITHUB_API" 2>/dev/null); then
        echo -e "${RED}Could not reach GitHub Releases API ($GITHUB_API).${NC}"
        echo "Check your network connection and try again."
        exit 1
    fi

    LATEST_VERSION=$(echo "$RELEASE_DATA" | grep '"tag_name"' | head -1 | cut -d '"' -f 4)
    DOWNLOAD_URL=$(echo "$RELEASE_DATA" | grep "browser_download_url.*${RELEASE_NAME}.${ARCHIVE_EXT}\"" | cut -d '"' -f 4)

    if [ -z "$DOWNLOAD_URL" ]; then
        echo -e "${RED}No pre-built binary for ${OS}-${ARCH} in the latest release.${NC}"
        echo "Available assets:"
        echo "$RELEASE_DATA" | grep '"name"' | grep -E 'ageni-' | cut -d '"' -f 4 | sed 's/^/  /'
        exit 1
    fi

    echo -e "${GREEN}Downloading version: $LATEST_VERSION${NC}"

    TEMP_DIR=$(mktemp -d)
    cd "$TEMP_DIR"

    if ! curl -fL --progress-bar "$DOWNLOAD_URL" -o "${RELEASE_NAME}.${ARCHIVE_EXT}"; then
        echo -e "${RED}Download failed.${NC}"
        restore_backup
        exit 1
    fi

    # Verify checksum if a sibling .sha256 asset exists.
    SHA_URL=$(echo "$RELEASE_DATA" | grep "browser_download_url.*${RELEASE_NAME}.${ARCHIVE_EXT}.sha256\"" | cut -d '"' -f 4)
    if [ -n "$SHA_URL" ]; then
        echo "Verifying checksum..."
        if curl -sfL "$SHA_URL" -o "${RELEASE_NAME}.${ARCHIVE_EXT}.sha256"; then
            EXPECTED=$(awk '{print $1}' "${RELEASE_NAME}.${ARCHIVE_EXT}.sha256")
            if command -v sha256sum >/dev/null 2>&1; then
                ACTUAL=$(sha256sum "${RELEASE_NAME}.${ARCHIVE_EXT}" | awk '{print $1}')
            else
                ACTUAL=$(shasum -a 256 "${RELEASE_NAME}.${ARCHIVE_EXT}" | awk '{print $1}')
            fi
            if [ "$EXPECTED" != "$ACTUAL" ]; then
                echo -e "${RED}Checksum mismatch (expected $EXPECTED, got $ACTUAL).${NC}"
                restore_backup
                exit 1
            fi
            echo -e "${GREEN}Checksum OK.${NC}"
        fi
    fi

    case "$ARCHIVE_EXT" in
        tar.gz) tar -xzf "${RELEASE_NAME}.${ARCHIVE_EXT}" ;;
        zip)    unzip -q "${RELEASE_NAME}.${ARCHIVE_EXT}" ;;
    esac

    if [ ! -f "$RELEASE_NAME" ]; then
        echo -e "${RED}Binary $RELEASE_NAME not found in archive.${NC}"
        restore_backup
        exit 1
    fi

    mv "$RELEASE_NAME" "$BINARY_NAME"
    chmod +x "$BINARY_NAME"

    echo -e "${GREEN}Download successful.${NC}"
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

main() {
    echo "=== ageni update ==="
    echo ""

    find_installation
    get_latest_version
    backup_current
    download_binary
    install_new_version
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
