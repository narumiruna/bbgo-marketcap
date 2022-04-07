package marketcap

import "github.com/c9s/bbgo/pkg/types"

func Normalize(s types.Float64Slice) types.Float64Slice {
	return s.DivScalar(s.Sum())
}
