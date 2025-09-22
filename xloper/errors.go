package xloper

import "errors"

var ErrInvalid = errors.New("invalid XLOPER")
var ErrBufferTooSmall = errors.New("buffer too small")
var ErrOutOfBounds = errors.New("out of excel bounds")
