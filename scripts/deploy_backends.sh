#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# Helper Script to deploy/remove test Docker backend containers
# ==============================================================================

ACTION="${1:-start}"
NUM_BACKENDS="${2:-3}"
IMAGE="hashicorp/http-echo:latest"
LABEL="lb.backend=true"

if [ "$ACTION" = "start" ]; then
    echo "[*] Starting $NUM_BACKENDS test backend containers..."
    for i in $(seq 1 "$NUM_BACKENDS"); do
        NAME="lb-backend-$i"
        # Stop and remove existing container if running
        docker rm -f "$NAME" 2>/dev/null || true
        
        echo " -> Launching $NAME on port 8080..."
        docker run -d \
            --name "$NAME" \
            --label "$LABEL" \
            "$IMAGE" -text="Hello from Backend #$i ($NAME)!" -listen=:8080
    done
    echo "[+] Done. Inspecting running backends:"
    docker ps --filter "label=$LABEL" --format "table {{.ID}}\t{{.Names}}\t{{.Status}}\t{{.Ports}}"

elif [ "$ACTION" = "stop" ]; then
    echo "[*] Stopping and removing test backend containers..."
    docker ps -a -q --filter "label=$LABEL" | xargs -r docker rm -f
    echo "[+] Test containers cleaned up."
else
    echo "Usage: $0 [start|stop] [num_backends]"
    exit 1
fi
