#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# Setup Script for XDP Load Balancer Dependencies (Ubuntu / Debian)
# ==============================================================================

echo "======================================================"
echo " Setting up XDP Load Balancer dependencies..."
echo "======================================================"

if ! command -v apt-get &>/dev/null; then
    echo "[ERROR] This script requires a Debian/Ubuntu system with apt-get."
    exit 1
fi

echo "[1/4] Updating package index..."
sudo apt-get update -y

echo "[2/4] Installing Clang, LLVM, libbpf-dev, and build essentials..."
sudo apt-get install -y \
    build-essential \
    clang \
    llvm \
    lld \
    libbpf-dev \
    pkg-config \
    make \
    curl \
    git \
    iproute2

echo "[3/4] Checking Go toolchain..."
if ! command -v go &>/dev/null; then
    echo "[*] Installing Go via apt..."
    sudo apt-get install -y golang-go
fi

echo "[4/4] Installing bpf2go code generator..."
GOPATH_BIN="$(go env GOPATH)/bin"
mkdir -p "${GOPATH_BIN}"
go install github.com/cilium/ebpf/cmd/bpf2go@latest

export PATH="${PATH}:${GOPATH_BIN}"
if [[ ":$PATH:" != *":${GOPATH_BIN}:"* ]]; then
    if ! grep -qs "${GOPATH_BIN}" ~/.bashrc; then
        echo "export PATH=\$PATH:${GOPATH_BIN}" >> ~/.bashrc
    fi
fi

echo ""
echo "======================================================"
echo " Dependencies Installed Successfully!"
echo "======================================================"
echo -n "  clang:   "; clang --version | head -n 1
echo -n "  go:      "; go version
echo -n "  bpf2go:  "; command -v bpf2go || echo "${GOPATH_BIN}/bpf2go"
echo "======================================================"
