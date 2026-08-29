#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# Helper Script to generate test traffic against the load balancer
# ==============================================================================

TARGET_HOST="${1:-127.0.0.1}"
TARGET_PORT="${2:-8080}"
REQUEST_COUNT="${3:-10}"

echo "======================================================"
echo " Sending $REQUEST_COUNT requests to http://${TARGET_HOST}:${TARGET_PORT}/"
echo "======================================================"

for i in $(seq 1 "$REQUEST_COUNT"); do
    echo -n "Request #$i: "
    curl -s --connect-timeout 2 "http://${TARGET_HOST}:${TARGET_PORT}/" || echo "Failed to connect"
    sleep 0.2
done

echo ""
echo "[+] Traffic test finished."
