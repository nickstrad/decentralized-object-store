package erasure

const (
	polynomial = 0x11D
	fieldOrder = 255
)

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

func Add(a, b byte) byte { return a ^ b }

func Mul(a, b byte) byte { return mulTable[a][b] }

func Inv(a byte) (byte, bool) {
	if a == 0 {
		return 0, false
	}
	return expTable[fieldOrder-logTable[a]], true
}

func Div(a, b byte) (byte, bool) {
	inv, ok := Inv(b)
	if !ok {
		return 0, false
	}
	return Mul(a, inv), true
}

func Exp(i int) byte {
	i %= fieldOrder
	if i < 0 {
		i += fieldOrder
	}
	return expTable[i]
}

func Log(a byte) (int, bool) {
	if a == 0 {
		return 0, false
	}
	return logTable[a], true
}

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

func mulSlice(c byte, src, dst []byte) {
	n := min(len(src), len(dst))
	src, dst = src[:n], dst[:n]
	t := &mulTable[c]
	for i, s := range src {
		dst[i] = t[s]
	}
}
