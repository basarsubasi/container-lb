#ifndef __BPF_ENDIAN_H__
#define __BPF_ENDIAN_H__

#include <linux/types.h>

#define ___constant_swab16(x) ((__u16)(				\
	(((__u16)(x) & (__u16)0x00ffU) << 8) |			\
	(((__u16)(x) & (__u16)0xff00U) >> 8)))

#define ___constant_swab32(x) ((__u32)(				\
	(((__u32)(x) & (__u32)0x000000ffUL) << 24) |		\
	(((__u32)(x) & (__u32)0x0000ff00UL) <<  8) |		\
	(((__u32)(x) & (__u32)0x00ff0000UL) >>  8) |		\
	(((__u32)(x) & (__u32)0xff000000UL) >> 24)))

#if __BYTE_ORDER__ == __ORDER_LITTLE_ENDIAN__
#define bpf_htons(x)				\
	(__builtin_constant_p(x) ?		\
	 ___constant_swab16(x) : __builtin_bswap16(x))
#define bpf_ntohs(x) bpf_htons(x)
#define bpf_htonl(x)				\
	(__builtin_constant_p(x) ?		\
	 ___constant_swab32(x) : __builtin_bswap32(x))
#define bpf_ntohl(x) bpf_htonl(x)
#elif __BYTE_ORDER__ == __ORDER_BIG_ENDIAN__
#define bpf_htons(x) (x)
#define bpf_ntohs(x) (x)
#define bpf_htonl(x) (x)
#define bpf_ntohl(x) (x)
#else
#error "Cannot determine target byte order!"
#endif

#endif /* __BPF_ENDIAN_H__ */
