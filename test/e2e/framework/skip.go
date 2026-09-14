package framework

import "fmt"

// SkipFunc skips the current Ginkgo spec. It is wired from the e2e suite so
// this package does not import github.com/onsi/ginkgo/v2 (framework is
// type-checked without the e2e build tag).
type SkipFunc func(message string)

var skipTest SkipFunc = func(message string) {
	panic("framework skip called before e2e suite init: " + message)
}

// SetSkipFunc registers the Ginkgo Skip helper used by SkipUnless* helpers.
func SetSkipFunc(f SkipFunc) {
	skipTest = f
}

func skipTestf(format string, args ...any) {
	skipTest(fmt.Sprintf(format, args...))
}
