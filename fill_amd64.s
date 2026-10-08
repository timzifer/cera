//go:build !purego

#include "textflag.h"

// func fillStream(p *byte, n int, v uint32)
//
// Sets n bytes from p to the repeated little-endian v with non-temporal
// stores. p is 16-byte aligned, n a multiple of 64. The stores are ordered
// before later ones only by storeFence.
TEXT ·fillStream(SB), NOSPLIT|NOFRAME, $0-20
	MOVQ p+0(FP), DI
	MOVQ n+8(FP), CX
	MOVL v+16(FP), AX
	MOVQ AX, X0
	PSHUFD $0, X0, X0
	TESTQ CX, CX
	JLE done

loop:
	MOVNTO X0, 0(DI)
	MOVNTO X0, 16(DI)
	MOVNTO X0, 32(DI)
	MOVNTO X0, 48(DI)
	ADDQ $64, DI
	SUBQ $64, CX
	JG loop

done:
	RET

// func storeFence()
TEXT ·storeFence(SB), NOSPLIT|NOFRAME, $0-0
	SFENCE
	RET
