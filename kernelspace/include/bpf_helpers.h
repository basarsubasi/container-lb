#ifndef __BPF_HELPERS_H__
#define __BPF_HELPERS_H__

#include <linux/types.h>
#include <linux/bpf.h>

#define SEC(NAME) __attribute__((section(NAME), used))

/* BPF Helper function pointers */
static void *(*bpf_map_lookup_elem)(void *map, const void *key) = (void *) BPF_FUNC_map_lookup_elem;
static long (*bpf_map_update_elem)(void *map, const void *key, const void *value, __u64 flags) = (void *) BPF_FUNC_map_update_elem;
static long (*bpf_map_delete_elem)(void *map, const void *key) = (void *) BPF_FUNC_map_delete_elem;
static __u32 (*bpf_get_prandom_u32)(void) = (void *) BPF_FUNC_get_prandom_u32;
static long (*bpf_redirect)(__u32 ifindex, __u64 flags) = (void *) BPF_FUNC_redirect;
static long (*bpf_trace_printk)(const char *fmt, __u32 fmt_size, ...) = (void *) BPF_FUNC_trace_printk;
static __s64 (*bpf_csum_diff)(__be32 *from, __u32 from_size, __be32 *to, __u32 to_size, __wsum seed) = (void *) BPF_FUNC_csum_diff;

#ifndef bpf_printk
#define bpf_printk(fmt, ...)				\
({							\
	char ____fmt[] = fmt;				\
	bpf_trace_printk(____fmt, sizeof(____fmt),	\
			 ##__VA_ARGS__);		\
})
#endif

#endif /* __BPF_HELPERS_H__ */
