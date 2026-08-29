#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# Setup Script for XDP Load Balancer Dependencies (Ubuntu / Debian)
#
# Installs:
#   - clang, llvm          → BPF C compilation
#   - libbpf-dev           → bpf/bpf_helpers.h, bpf/bpf_endian.h
#   - linux-tools           → bpftool (for vmlinux.h generation from kernel BTF)
#   - iproute2, build tools → general utilities
#   - Go toolchain + bpf2go → userspace control plane + BPF code generation
# ==============================================================================

echo "======================================================"
echo " Setting up XDP Load Balancer dependencies..."
echo "======================================================"

if ! command -v apt-get &>/dev/null; then
    echo "[ERROR] This script requires a Debian/Ubuntu system with apt-get."
    exit 1
fi

echo "[1/5] Updating package index..."
sudo apt-get update -y

echo "[2/5] Installing clang, llvm, libbpf-dev, and build essentials..."
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

echo "[3/5] Installing bpftool..."
# bpftool lives in linux-tools-$(uname -r) on Ubuntu/Debian.
KVER=$(uname -r)
if ! command -v bpftool &>/dev/null; then
    echo "    Installing linux-tools-common and linux-tools-${KVER}..."
    sudo apt-get install -y linux-tools-common "linux-tools-${KVER}" 2>/dev/null || {
        echo "    [WARN] Could not install linux-tools-${KVER}."
        echo "    Trying linux-tools-generic as fallback..."
        sudo apt-get install -y linux-tools-generic 2>/dev/null || {
            echo "    [WARN] Could not install bpftool via apt."
            echo "    You may need to install bpftool manually."
            echo "    See: https://github.com/libbpf/bpftool"
        }
    }
fi

echo "[4/5] Checking Go toolchain..."
if ! command -v go &>/dev/null; then
    echo "    Installing Go via apt..."
    sudo apt-get install -y golang-go
fi

echo "[5/5] Installing bpf2go code generator..."
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
echo -n "  clang:    "; clang --version 2>/dev/null | head -n 1 || echo "NOT FOUND"
echo -n "  bpftool:  "; bpftool version 2>/dev/null || echo "NOT FOUND"
echo -n "  go:       "; go version
echo -n "  bpf2go:   "; command -v bpf2go || echo "${GOPATH_BIN}/bpf2go"

# Verify kernel BTF is available (required for vmlinux.h generation).
if [ -f /sys/kernel/btf/vmlinux ]; then
    echo "  BTF:      /sys/kernel/btf/vmlinux ✓"
else
    echo ""
    echo "  [WARN] /sys/kernel/btf/vmlinux not found."
    echo "  Your kernel must have CONFIG_DEBUG_INFO_BTF=y for vmlinux.h generation."
fi
echo "======================================================"
