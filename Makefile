GO     := go
CLANG  := clang
BPF2GO := bpf2go
ARCH   := $(shell uname -m)

INCLUDES := -I./kernelspace -I/usr/include/$(ARCH)-linux-gnu

.PHONY: generate build clean run backends-up backends-down

generate:
	cd userspace/bpf && \
		GOPACKAGE=bpf GOARCH=amd64 $(BPF2GO) \
			-cc $(CLANG) \
			-go-package bpf \
			-type backend_info \
			-type lb_config \
			-type stats_counter \
			lb ../../kernelspace/xdp_lb.c \
			-- -O2 -g -Wall $(INCLUDES)

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
