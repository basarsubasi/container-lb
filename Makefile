CC      ?= clang
GO      ?= go
BPFTOOL ?= bpf2go

# Detect the multiarch include directory (e.g. /usr/include/x86_64-linux-gnu).
# This is required because clang does not add arch-specific paths when
# cross-compiling to the BPF target, so headers like <asm/types.h> would
# otherwise be missing.
ARCH_INCLUDES := /usr/include/$(shell uname -m)-linux-gnu

# Extra CFLAGS forwarded to clang by bpf2go.
BPF_CFLAGS := -O2 -g -Wall -I./kernelspace -I$(ARCH_INCLUDES)

.PHONY: all generate build clean test backends-up backends-down test-traffic run

all: generate build

## Compile the BPF C source and generate Go bindings via bpf2go.
## bpf2go is invoked directly so we can control include paths precisely.
## GOPACKAGE must be set explicitly when calling bpf2go outside of `go generate`.
generate:
	@echo "==> Generating eBPF bindings (clang -> bpf2go)..."
	@echo "    arch includes: $(ARCH_INCLUDES)"
	cd userspace/bpf && GOPACKAGE=bpf $(BPFTOOL) \
		-cc $(CC) \
		-go-package bpf \
		-type backend_info \
		-type lb_config \
		-type stats_counter \
		lb \
		../../kernelspace/xdp_lb.c \
		-- $(BPF_CFLAGS)

## Build the userspace control plane binary.
build:
	@echo "==> Building container-lb binary..."
	mkdir -p bin
	$(GO) build -v -o bin/container-lb ./userspace/cmd

## Remove generated bindings and build artifacts.
clean:
	@echo "==> Cleaning artifacts..."
	rm -rf bin/
	rm -f userspace/bpf/lb_bpfel.go userspace/bpf/lb_bpfel.o
	rm -f userspace/bpf/lb_bpfeb.go userspace/bpf/lb_bpfeb.o

## Run unit tests.
test:
	$(GO) test -v ./...

## Spin up test Docker backend containers.
backends-up:
	./scripts/deploy_backends.sh start 3

## Tear down test Docker backend containers.
backends-down:
	./scripts/deploy_backends.sh stop

## Send test HTTP traffic to the load balancer.
test-traffic:
	./scripts/test_traffic.sh 127.0.0.1 8080 10

## Run the load balancer (requires sudo for XDP/eBPF access).
run: build
	@echo "==> Running container-lb (sudo required)..."
	sudo ./bin/container-lb -iface docker0 -port 8080
