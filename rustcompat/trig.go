package rustcompat

// Single-precision sine and cosine as Rust's f32::sin and f32::cos compute them on Linux x86-64:
// they call the platform's sinf and cosf, which glibc implements with Arm's optimized-routines
// algorithm (a double-precision polynomial after range reduction; worst case 0.56 ULP, so not
// correctly rounded, unlike float32(math.Sin(float64(x)))). glibc picks its FMA build on CPUs
// with FMA, where the compiler fuses the polynomial's multiply-adds; math.FMA reproduces that, and
// makes the result the same on every platform.
//
// Ported from Arm Optimized Routines (math/sinf.c, math/cosf.c, math/sincosf.h,
// math/sincosf_data.c), Copyright (c) 2018-2024, Arm Limited, MIT OR Apache-2.0 WITH
// LLVM-exception; see licenses/arm-optimized-routines/LICENSE.

import "math"

type sincos struct {
	sign               [4]float64
	hpiInv, hpi        float64
	c0, c1, c2, c3, c4 float64
	s1, s2, s3         float64
}

// sincosTable is the polynomial data; the second entry computes -cos.
var sincosTable = [2]sincos{
	{
		[4]float64{1.0, -1.0, -1.0, 1.0},
		0x1.45F306DC9C883p+23,
		0x1.921FB54442D18p0,
		0x1p0, -0x1.ffffffd0c621cp-2, 0x1.55553e1068f19p-5, -0x1.6c087e89a359dp-10, 0x1.99343027bf8c3p-16,
		-0x1.555545995a603p-3, 0x1.1107605230bc4p-7, -0x1.994eb3774cf24p-13,
	},
	{
		[4]float64{1.0, -1.0, -1.0, 1.0},
		0x1.45F306DC9C883p+23,
		0x1.921FB54442D18p0,
		-0x1p0, 0x1.ffffffd0c621cp-2, -0x1.55553e1068f19p-5, 0x1.6c087e89a359dp-10, -0x1.99343027bf8c3p-16,
		-0x1.555545995a603p-3, 0x1.1107605230bc4p-7, -0x1.994eb3774cf24p-13,
	},
}

// invPio4 is 4/π to 192 bits; 8 new bits per entry.
var invPio4 = [24]uint32{
	0xa2, 0xa2f9, 0xa2f983, 0xa2f9836e,
	0xf9836e4e, 0x836e4e44, 0x6e4e4415, 0x4e441529,
	0x441529fc, 0x1529fc27, 0x29fc2757, 0xfc2757d1,
	0x2757d1f5, 0x57d1f534, 0xd1f534dd, 0xf534ddc0,
	0x34ddc0db, 0xddc0db62, 0xc0db6295, 0xdb629599,
	0x6295993c, 0x95993c43, 0x993c4390, 0x3c439041,
}

const (
	pi63  = 0x1.921FB54442D18p-62 // 2π · 2^-64
	pio4f = float32(0x1.921FB6p-1)
)

// abstop12 is the top 12 bits of x's representation with the sign cleared.
func abstop12(x float32) uint32 { return (math.Float32bits(x) >> 20) & 0x7ff }

// sinfPoly is the sine of x (x2 its square) with polynomial p; for an odd quadrant n, the cosine.
func sinfPoly(x, x2 float64, p *sincos, n int) float32 {
	if n&1 == 0 {
		x3 := x * x2
		s1 := math.FMA(x2, p.s3, p.s2)
		x7 := x3 * x2
		s := math.FMA(x3, p.s1, x)
		return float32(math.FMA(x7, s1, s))
	}
	x4 := x2 * x2
	c2 := math.FMA(x2, p.c4, p.c3)
	c1 := math.FMA(x2, p.c1, p.c0)
	x6 := x4 * x2
	c := math.FMA(x4, p.c2, c1)
	return float32(math.FMA(x6, c2, c))
}

// reduceFast reduces x (|x| ≤ 120) to [-π/4, π/4] with one multiply-subtract, and the quadrant.
func reduceFast(x float64, p *sincos) (float64, int) {
	r := x * p.hpiInv
	n := (int32(r) + 0x800000) >> 24
	return math.FMA(-float64(n), p.hpi, x), int(n)
}

// reduceLarge reduces the float with bits xi (|x| ≥ 2, sign ignored) to [-π/4, π/4] with 4/π
// to 192 bits, and the quadrant.
func reduceLarge(xi uint32) (float64, int) {
	k := (xi >> 26) & 15
	shift := (xi >> 23) & 7
	xi = (xi & 0xffffff) | 0x800000
	xi <<= shift
	res0 := uint64(xi * invPio4[k])
	res1 := uint64(xi) * uint64(invPio4[k+4])
	res2 := uint64(xi) * uint64(invPio4[k+8])
	res0 = (res2 >> 32) | (res0 << 32)
	res0 += res1
	n := (res0 + (1 << 61)) >> 62
	res0 -= n << 62
	return float64(int64(res0)) * pi63, int(n)
}

// sinCos computes sin(y) (cos false) or cos(y).
func sinCos(y float32, cos bool) float32 {
	x := float64(y)
	p := &sincosTable[0]
	q := 0
	if cos {
		q = 1
	}
	switch top := abstop12(y); {
	case top < abstop12(pio4f):
		if top < abstop12(0x1p-12) {
			if cos {
				return 1
			}
			return y
		}
		return sinfPoly(x, x*x, p, q)
	case top < abstop12(120):
		x, n := reduceFast(x, p)
		s := p.sign[n&3]
		if n&2 != 0 {
			p = &sincosTable[1]
		}
		return sinfPoly(x*s, x*x, p, n^q)
	case top < abstop12(float32(math.Inf(1))):
		xi := math.Float32bits(y)
		sign := int(xi >> 31)
		x, n := reduceLarge(xi)
		s := p.sign[(n+sign)&3]
		if (n+sign)&2 != 0 {
			p = &sincosTable[1]
		}
		return sinfPoly(x*s, x*x, p, n^q)
	}
	return float32(math.NaN())
}

// Sin32 is Rust's f32::sin on Linux x86-64 (glibc's sinf).
func Sin32(y float32) float32 { return sinCos(y, false) }

// Cos32 is Rust's f32::cos on Linux x86-64 (glibc's cosf).
func Cos32(y float32) float32 { return sinCos(y, true) }
