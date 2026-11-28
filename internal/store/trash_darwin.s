#include "textflag.h"

TEXT ·libc_renameatx_np_trampoline(SB),NOSPLIT,$0-0
	JMP	libc_renameatx_np(SB)

GLOBL	·libc_renameatx_np_trampoline_addr(SB), RODATA, $8
DATA	·libc_renameatx_np_trampoline_addr(SB)/8, $·libc_renameatx_np_trampoline(SB)

TEXT ·libc_unlinkat_trampoline(SB),NOSPLIT,$0-0
	JMP	libc_unlinkat(SB)

GLOBL	·libc_unlinkat_trampoline_addr(SB), RODATA, $8
DATA	·libc_unlinkat_trampoline_addr(SB)/8, $·libc_unlinkat_trampoline(SB)
