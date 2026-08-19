package cni

import (
	"errors"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RawExec", func() {
	Context("pluginErr", func() {
		It("formats error messages correctly", func() {
			e := &RawExec{}
			err := e.pluginErr(errors.New("boom"), nil, []byte("some-stderr"))
			Expect(err.Error()).To(ContainSubstring("netplugin failed"))
		})
	})

	Context("baseExecEnv", func() {
		It("forwards only PATH when present", func() {
			originalPath, hadPath := os.LookupEnv("PATH")
			originalHome, hadHome := os.LookupEnv("HOME")
			Expect(os.Setenv("PATH", "/test/bin")).To(Succeed())
			Expect(os.Setenv("HOME", "/secret/home")).To(Succeed())
			DeferCleanup(func() {
				if hadPath {
					Expect(os.Setenv("PATH", originalPath)).To(Succeed())
				} else {
					Expect(os.Unsetenv("PATH")).To(Succeed())
				}
				if hadHome {
					Expect(os.Setenv("HOME", originalHome)).To(Succeed())
				} else {
					Expect(os.Unsetenv("HOME")).To(Succeed())
				}
			})

			Expect(baseExecEnv()).To(Equal([]string{"PATH=/test/bin"}))
		})
	})
})
