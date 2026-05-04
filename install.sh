#!/bin/bash
set -e

# ageni installation script.
# Detects platform, downloads the latest release, falls back to building from
# source if no pre-built binary is available.

REPO_URL="https://github.com/bouwerp/ageni"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
BINARY_NAME="ageni"
GITHUB_API="https://api.github.com/repos/bouwerp/ageni/releases/latest"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)

    case "$ARCH" in
        x86_64) ARCH="amd64" ;;
        arm64|aarch64) ARCH="arm64" ;;
        *)
            echo -e "${RED}Unsupported architecture: $ARCH${NC}"
            exit 1
            ;;
    esac

    case "$OS" in
        linux|darwin)
            PLATFORM="${OS}_${ARCH}"
            ;;
        *)
            echo -e "${RED}Unsupported operating system: $OS${NC}"
            exit 1
            ;;
    esac

    echo -e "${GREEN}Detected platform: $PLATFORM${NC}"
}

get_release_name() {
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
    echo "Checking for pre-built binary..."

    get_release_name

    if ! RELEASE_DATA=$(curl -sf "$GITHUB_API" 2>/dev/null); then
        echo -e "${YELLOW}No release found, will build from source${NC}"
        return 1
    fi

    DOWNLOAD_URL=$(echo "$RELEASE_DATA" | grep "browser_download_url.*${RELEASE_NAME}.${ARCHIVE_EXT}\"" | cut -d '"' -f 4)

    if [ -z "$DOWNLOAD_URL" ]; then
        echo -e "${YELLOW}Pre-built binary not available for $PLATFORM, will build from source${NC}"
        return 1
    fi

    VERSION=$(echo "$RELEASE_DATA" | grep '"tag_name"' | cut -d '"' -f 4)
    echo -e "${GREEN}Found release: $VERSION${NC}"
    echo "Downloading $RELEASE_NAME..."

    TEMP_DIR=$(mktemp -d)
    cd "$TEMP_DIR"

    if ! curl -sfL "$DOWNLOAD_URL" -o "${RELEASE_NAME}.${ARCHIVE_EXT}"; then
        echo -e "${RED}Download failed${NC}"
        return 1
    fi

    echo "Extracting binary..."
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

check_prerequisites() {
    echo "Checking prerequisites..."

    if ! command -v git &> /dev/null; then
        echo -e "${RED}Error: git is required but not installed.${NC}"
        exit 1
    fi

    if ! command -v go &> /dev/null; then
        echo -e "${YELLOW}Warning: Go is not installed. Attempting to install...${NC}"
        install_go
    else
        GO_VERSION=$(go version | grep -oE '[0-9]+\.[0-9]+' | head -1)
        echo -e "${GREEN}Go version: $GO_VERSION${NC}"
    fi
}

install_go() {
    echo "Installing Go..."

    case "$OS" in
        linux)
            if command -v apt-get &> /dev/null; then
                sudo apt-get update && sudo apt-get install -y golang-go
            elif command -v yum &> /dev/null; then
                sudo yum install -y golang
            elif command -v pacman &> /dev/null; then
                sudo pacman -S go
            else
                echo -e "${RED}Could not install Go automatically. Please install Go 1.24+ manually.${NC}"
                exit 1
            fi
            ;;
        darwin)
            if command -v brew &> /dev/null; then
                brew install go
            else
                echo -e "${YELLOW}Homebrew not found. Installing Homebrew first...${NC}"
                /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
                brew install go
            fi
            ;;
    esac
}

clone_repo() {
    echo "Cloning repository..."
    TEMP_DIR=$(mktemp -d)
    cd "$TEMP_DIR"
    git clone --depth 1 "$REPO_URL" ageni-src
    cd ageni-src
}

build_binary() {
    echo "Building ageni for $PLATFORM..."

    export GOOS="$OS"
    export GOARCH="$ARCH"

    VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
    BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

    go build -ldflags "-X main.version=$VERSION -X main.buildTime=$BUILD_TIME" \
        -o "${BINARY_NAME}" \
        ./cmd/ageni

    if [ $? -ne 0 ]; then
        echo -e "${RED}Build failed!${NC}"
        exit 1
    fi

    echo -e "${GREEN}Build successful!${NC}"
}

install_binary() {
    echo "Installing ageni to $INSTALL_DIR..."

    mkdir -p "$INSTALL_DIR"

    if [ -w "$INSTALL_DIR" ]; then
        mv "${BINARY_NAME}" "$INSTALL_DIR/"
    else
        echo -e "${YELLOW}Requesting sudo access to install to $INSTALL_DIR${NC}"
        sudo mv "${BINARY_NAME}" "$INSTALL_DIR/"
    fi

    chmod +x "$INSTALL_DIR/$BINARY_NAME"

    ensure_path

    if command -v ageni &> /dev/null || [ -x "$INSTALL_DIR/$BINARY_NAME" ]; then
        echo -e "${GREEN}Installation successful!${NC}"
        echo ""
        echo "ageni is now installed at: $INSTALL_DIR/$BINARY_NAME"
        echo ""
        INSTALLED_VERSION=$("$INSTALL_DIR/$BINARY_NAME" --version 2>/dev/null || echo "unknown")
        echo "Version: $INSTALLED_VERSION"
        echo ""
        echo "Next steps:"
        echo "  1. Copy .env.example to .env and add your API keys"
        echo "  2. Run 'ageni' to start the TUI"
    else
        echo -e "${YELLOW}Warning: ageni was installed but could not be verified${NC}"
        echo "Binary location: $INSTALL_DIR/$BINARY_NAME"
    fi
}

ensure_path() {
    case ":$PATH:" in
        *":$INSTALL_DIR:"*) return ;;
    esac

    SHELL_NAME=$(basename "${SHELL:-/bin/bash}")
    case "$SHELL_NAME" in
        zsh)  PROFILE="$HOME/.zshrc" ;;
        bash)
            if [ "$OS" = "darwin" ] && [ -f "$HOME/.bash_profile" ]; then
                PROFILE="$HOME/.bash_profile"
            else
                PROFILE="$HOME/.bashrc"
            fi
            ;;
        *)    PROFILE="$HOME/.profile" ;;
    esac

    PATH_LINE="export PATH=\"\$PATH:$INSTALL_DIR\""

    if [ -f "$PROFILE" ] && grep -qF "$INSTALL_DIR" "$PROFILE" 2>/dev/null; then
        return
    fi

    echo "" >> "$PROFILE"
    echo "# Added by ageni installer" >> "$PROFILE"
    echo "$PATH_LINE" >> "$PROFILE"
    echo -e "${GREEN}Added $INSTALL_DIR to PATH in $PROFILE${NC}"
    echo -e "${YELLOW}Restart your shell or run: source $PROFILE${NC}"

    export PATH="$PATH:$INSTALL_DIR"
}

setup_config() {
    echo "Setting up configuration directory..."
    CONFIG_DIR="$HOME/.ageni"

    if [ ! -d "$CONFIG_DIR" ]; then
        mkdir -p "$CONFIG_DIR/sessions"
        echo -e "${GREEN}Created $CONFIG_DIR${NC}"
    fi

    echo "Configuration will be stored in: $CONFIG_DIR"
}

cleanup() {
    if [ -n "$TEMP_DIR" ] && [ -d "$TEMP_DIR" ]; then
        rm -rf "$TEMP_DIR"
    fi
}

trap cleanup EXIT

main() {
    echo "=== ageni installation ==="
    echo ""

    detect_platform
    setup_config

    if download_binary; then
        install_binary
    else
        echo ""
        echo -e "${YELLOW}Falling back to building from source...${NC}"
        check_prerequisites
        clone_repo
        build_binary
        install_binary
    fi

    echo ""
    echo -e "${GREEN}Installation complete!${NC}"
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --prefix)
            INSTALL_DIR="$2"
            shift 2
            ;;
        --system)
            INSTALL_DIR="/usr/local/bin"
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --prefix DIR    Install to custom directory (default: ~/.local/bin)"
            echo "  --system        Install to /usr/local/bin (requires sudo)"
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
