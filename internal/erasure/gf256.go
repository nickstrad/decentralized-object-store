package erasure

// Field parameters for GF(2^8): primitive polynomial x^8 + x^4 + x^3 + x^2 + 1
// (0x11D) with generator 2, whose multiplicative group has order 255.
const (
	polynomial = 0x11D
	fieldOrder = 255
)

// expTable is doubled to 2*fieldOrder entries so expTable[log(a)+log(b)] needs
// no modulo reduction. mulTable caches all 65,536 byte products for fast slice
// multiplication.
var (
	expTable [2 * fieldOrder]byte
	logTable [256]int
	mulTable = buildTables()
)

func buildTables() *[256][256]byte {
	x := 1
	for i := 0; i < fieldOrder; i++ {
		expTable[i] = byte(x)
		expTable[i+fieldOrder] = byte(x)
		logTable[x] = i
		x <<= 1
		if x&0x100 != 0 {
			x ^= polynomial
		}
	}
	t := new([256][256]byte)
	for a := 1; a < 256; a++ {
		for b := 1; b < 256; b++ {
			t[a][b] = expTable[logTable[a]+logTable[b]]
		}
	}
	return t
}

// Add returns a + b in GF(2^8) (bitwise XOR; subtraction is identical).
func Add(a, b byte) byte { return a ^ b }

// Mul returns the product a * b in GF(2^8).
func Mul(a, b byte) byte { return mulTable[a][b] }

// Inv returns the multiplicative inverse of a in GF(2^8). It returns (0, false)
// when a is 0, which has no inverse.
func Inv(a byte) (byte, bool) {
	if a == 0 {
		return 0, false
	}
	return expTable[fieldOrder-logTable[a]], true
}

// Div returns a / b in GF(2^8), or (0, false) when b is 0.
func Div(a, b byte) (byte, bool) {
	inv, ok := Inv(b)
	if !ok {
		return 0, false
	}
	return Mul(a, inv), true
}

// Exp returns 2^i in GF(2^8) for any signed exponent i, reduced modulo 255.
func Exp(i int) byte {
	i %= fieldOrder
	if i < 0 {
		i += fieldOrder
	}
	return expTable[i]
}

// Log returns the discrete logarithm base 2 of a in [0, 254], or (0, false)
// when a is 0.
func Log(a byte) (int, bool) {
	if a == 0 {
		return 0, false
	}
	return logTable[a], true
}

// MulAddSlice computes dst[i] ^= c * src[i] in GF(2^8) over the shorter length
// of src and dst.
func MulAddSlice(c byte, src, dst []byte) {
	n := min(len(src), len(dst))
	src, dst = src[:n], dst[:n]
	switch c {
	case 0:
		return
	case 1:
		for i, s := range src {
			dst[i] ^= s
		}
		return
	}
	t := &mulTable[c]
	for i, s := range src {
		dst[i] ^= t[s]
	}
}

// mulSlice sets dst[i] = c * src[i] in GF(2^8) over the shorter length.
func mulSlice(c byte, src, dst []byte) {
	n := min(len(src), len(dst))
	src, dst = src[:n], dst[:n]
	t := &mulTable[c]
	for i, s := range src {
		dst[i] = t[s]
	}
}
