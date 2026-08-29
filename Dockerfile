# syntax=docker/dockerfile:1

# ============================================================================
# Stage 1: build the userspace binary.
# The XDP objects (userspace/bpf/lb_bpf*.o) are pre-generated via
# `make generate` and committed; they are embedded into the binary at
# compile time (go:embed), so no clang/bpftool is needed inside the image.
# Targets amd64, matching the pre-generated BPF objects.
# ============================================================================
FROM golang:1.25-alpine AS builder

WORKDIR /src

# Cache module downloads independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" \
    -o /out/container-lb ./userspace/cmd

# ============================================================================
# Stage 2: minimal runtime image (distroless, static).
# The binary is static (CGO disabled) and self-contained: the BPF programs
# and maps are embedded, so the image ships a single executable.
# distroless/static already ships ca-certificates (for DOCKER_HOST over
# TLS) and contains no shell or package manager to keep the attack
# surface minimal.
#
# NOTE: this image must be run with enough privileges to load/attach XDP
# programs and to inspect container netns, e.g.:
#
#   docker run --rm -it --privileged --network host --pid host \
#     -v /sys:/sys:ro -v /sys/fs/bpf:/sys/fs/bpf \
#     -v /var/run/docker.sock:/var/run/docker.sock \
#     <image> -iface docker0 -label lb.backend=true
# ============================================================================
FROM gcr.io/distroless/static-debian13

COPY --from=builder /out/container-lb /usr/local/bin/container-lb

# Loading/attaching XDP requires real root: use the default (root) variant,
# NOT the :nonroot tag. Distroless has no shell, so exec-form entrypoint.
ENTRYPOINT ["/usr/local/bin/container-lb"]
