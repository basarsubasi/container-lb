// Package bpf contains the BPF object loader, map helpers, and the bpf2go
// code generation directive.
//
// Run `make generate` from the project root to (re-)compile kernelspace/xdp_lb.c
// and regenerate the lb_bpfel.go / lb_bpfeb.go Go bindings.
//
// The Makefile handles the arch-specific include paths (-I/usr/include/<arch>-linux-gnu)
// required to compile the BPF C source on Debian/Ubuntu systems.
package bpf
