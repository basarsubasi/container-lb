GO     := go
CLANG  := clang
BPF2GO := bpf2go
ARCH   := $(shell uname -m)

BPFTOOL ?= ./tools/bpftool

# Root-privileged test runner: tests attach XDP programs and inspect container
# netns, which requires root. sudo sanitizes the environment, so PATH and the
# Go caches are re-exported explicitly. GOCACHE is redirected to /tmp so root
# never leaves root-owned files inside the user's build cache.
GOMODCACHE := $(shell $(GO) env GOMODCACHE)
SUDO_GO    := sudo env \
              "PATH=$(PATH):/usr/local/go/bin" \
              "HOME=$(HOME)" \
              "GOCACHE=/tmp/container-lb-gocache-root" \
              "GOMODCACHE=$(GOMODCACHE)" \
              $(GO)

.PHONY: generate build clean run test-integration test-e2e backends-up backends-down vmlinux

vmlinux:
	@echo "==> Generating vmlinux.h from BTF..."
	$(BPFTOOL) btf dump file /sys/kernel/btf/vmlinux format c > kernelspace/vmlinux.h

generate: vmlinux
	cd userspace/bpf && \
		GOPACKAGE=bpf GOARCH=amd64 $(BPF2GO) \
			-cc $(CLANG) \
			-go-package bpf \
			-type backend_info \
			-type lb_config \
			-type stats_counter \
			lb ../../kernelspace/xdp_lb.c \
			-- -O2 -g -Wall -I../../kernelspace

build:
	mkdir -p bin
	$(GO) build -v -o bin/container-lb ./userspace/cmd

clean:
	rm -rf bin
	rm -f userspace/bpf/lb_bpf*.go userspace/bpf/lb_bpf*.o

backends-up:
	./scripts/deploy_backends.sh start 3

backends-down:
	./scripts/deploy_backends.sh stop

run: build
	sudo ./bin/container-lb -iface docker0

test-integration:
	$(SUDO_GO) test -v -count=1 -timeout 120s ./tests/integration/

test-e2e: build
	$(SUDO_GO) test -v -count=1 -timeout 300s ./tests/e2e/
