package flags_test

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/urfave/cli/v2"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/flags"
)

var _ = Describe("Flags", func() {
	var kubeClientConfig *flags.KubeClientConfig

	BeforeEach(func() {
		kubeClientConfig = &flags.KubeClientConfig{}
	})

	Context("KubeClientConfig", func() {
		It("should create flags with correct defaults", func() {
			cliFlags := kubeClientConfig.Flags()
			Expect(len(cliFlags)).To(Equal(3))

			// Find each flag by name
			var kubeconfigFlag *cli.StringFlag
			var qpsFloat64Flag *cli.Float64Flag
			var burstIntFlag *cli.IntFlag

			for _, flag := range cliFlags {
				switch flag.Names()[0] {
				case "kubeconfig":
					kubeconfigFlag = flag.(*cli.StringFlag)
				case "kube-api-qps":
					qpsFloat64Flag = flag.(*cli.Float64Flag)
				case "kube-api-burst":
					burstIntFlag = flag.(*cli.IntFlag)
				}
			}

			// Verify kubeconfig flag
			Expect(kubeconfigFlag).NotTo(BeNil())
			Expect(kubeconfigFlag.Name).To(Equal("kubeconfig"))
			Expect(kubeconfigFlag.EnvVars).To(ContainElement("KUBECONFIG"))
			Expect(kubeconfigFlag.Category).To(Equal("Kubernetes client:"))

			// Verify QPS flag
			Expect(qpsFloat64Flag).NotTo(BeNil())
			Expect(qpsFloat64Flag.Name).To(Equal("kube-api-qps"))
			Expect(qpsFloat64Flag.Value).To(Equal(float64(5)))
			Expect(qpsFloat64Flag.EnvVars).To(ContainElement("KUBE_API_QPS"))
			Expect(qpsFloat64Flag.Category).To(Equal("Kubernetes client:"))

			// Verify Burst flag
			Expect(burstIntFlag).NotTo(BeNil())
			Expect(burstIntFlag.Name).To(Equal("kube-api-burst"))
			Expect(burstIntFlag.Value).To(Equal(10))
			Expect(burstIntFlag.EnvVars).To(ContainElement("KUBE_API_BURST"))
			Expect(burstIntFlag.Category).To(Equal("Kubernetes client:"))
		})

		It("should set destination fields correctly", func() {
			cliFlags := kubeClientConfig.Flags()

			// Create a mock CLI app to test flag parsing
			app := &cli.App{
				Name:  "test",
				Flags: cliFlags,
				Action: func(c *cli.Context) error {
					return nil
				},
			}

			// Test with custom values
			err := app.Run([]string{"test", "--kubeconfig", "/custom/path", "--kube-api-qps", "10.5", "--kube-api-burst", "20"})
			Expect(err).NotTo(HaveOccurred())

			Expect(kubeClientConfig.KubeConfig).To(Equal("/custom/path"))
			Expect(kubeClientConfig.KubeAPIQPS).To(Equal(10.5))
			Expect(kubeClientConfig.KubeAPIBurst).To(Equal(20))
		})

		It("should use environment variables", func() {
			// Set environment variables
			os.Setenv("KUBECONFIG", "/env/kubeconfig")
			os.Setenv("KUBE_API_QPS", "15.5")
			os.Setenv("KUBE_API_BURST", "25")

			defer func() {
				os.Unsetenv("KUBECONFIG")
				os.Unsetenv("KUBE_API_QPS")
				os.Unsetenv("KUBE_API_BURST")
			}()

			cliFlags := kubeClientConfig.Flags()

			// Create a mock CLI app to test environment variable parsing
			app := &cli.App{
				Name:  "test",
				Flags: cliFlags,
				Action: func(c *cli.Context) error {
					return nil
				},
			}

			// Run without explicit flags to test env vars
			err := app.Run([]string{"test"})
			Expect(err).NotTo(HaveOccurred())

			Expect(kubeClientConfig.KubeConfig).To(Equal("/env/kubeconfig"))
			Expect(kubeClientConfig.KubeAPIQPS).To(Equal(15.5))
			Expect(kubeClientConfig.KubeAPIBurst).To(Equal(25))
		})
	})

	Context("NewClientSetConfig", func() {
		It("should create in-cluster config when kubeconfig is empty", func() {
			kubeClientConfig.KubeConfig = ""
			kubeClientConfig.KubeAPIQPS = 10.0
			kubeClientConfig.KubeAPIBurst = 20

			// This will fail in test environment but we can test the error handling
			config, err := kubeClientConfig.NewClientSetConfig()

			// In test environment, in-cluster config will fail
			// but we verify it attempted in-cluster config
			if err != nil {
				Expect(err.Error()).To(ContainSubstring("in-cluster"))
			} else {
				// If somehow we're in a cluster environment
				Expect(config).NotTo(BeNil())
				Expect(config.QPS).To(Equal(float32(10.0)))
				Expect(config.Burst).To(Equal(20))
			}
		})

		It("should handle invalid kubeconfig path", func() {
			kubeClientConfig.KubeConfig = "/non/existent/path"
			kubeClientConfig.KubeAPIQPS = 5.0
			kubeClientConfig.KubeAPIBurst = 10

			_, err := kubeClientConfig.NewClientSetConfig()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("out-of-cluster"))
		})

		It("should set QPS and Burst values correctly", func() {
			// Create a temporary valid-looking kubeconfig file
			tempFile, err := os.CreateTemp("", "kubeconfig-test-*")
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(tempFile.Name())

			// Write minimal kubeconfig content
			kubeconfigContent := `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://test-server
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
  user: {}
`
			_, err = tempFile.WriteString(kubeconfigContent)
			Expect(err).NotTo(HaveOccurred())
			tempFile.Close()

			kubeClientConfig.KubeConfig = tempFile.Name()
			kubeClientConfig.KubeAPIQPS = 15.5
			kubeClientConfig.KubeAPIBurst = 30

			config, err := kubeClientConfig.NewClientSetConfig()
			Expect(err).NotTo(HaveOccurred())
			Expect(config).NotTo(BeNil())
			Expect(config.QPS).To(Equal(float32(15.5)))
			Expect(config.Burst).To(Equal(30))
		})
	})

	Context("NewClientSets", func() {
		It("should handle config creation failure", func() {
			kubeClientConfig.KubeConfig = "/invalid/path"

			_, err := kubeClientConfig.NewClientSets()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("client configuration"))
		})

		It("should create ClientSets with valid config", func() {
			// Create a temporary valid-looking kubeconfig file
			tempFile, err := os.CreateTemp("", "kubeconfig-test-*")
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(tempFile.Name())

			// Write minimal kubeconfig content
			kubeconfigContent := `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://test-server
    insecure-skip-tls-verify: true
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
  user: {}
`
			_, err = tempFile.WriteString(kubeconfigContent)
			Expect(err).NotTo(HaveOccurred())
			tempFile.Close()

			kubeClientConfig.KubeConfig = tempFile.Name()
			kubeClientConfig.KubeAPIQPS = 5.0
			kubeClientConfig.KubeAPIBurst = 10

			clientSets, err := kubeClientConfig.NewClientSets()
			Expect(err).NotTo(HaveOccurred())
			Expect(clientSets.Interface).NotTo(BeNil())
			Expect(clientSets.Client).NotTo(BeNil())
		})
	})

	Context("Scheme registration", func() {
		It("should have registered required schemes", func() {
			scheme := flags.Scheme
			Expect(scheme).NotTo(BeNil())

			// Test that the scheme knows about the registered types
			// We can't directly test scheme contents easily, but we can verify
			// the scheme is properly initialized
			Expect(scheme.AllKnownTypes()).NotTo(BeEmpty())
		})
	})

	Context("ClientSets type", func() {
		It("should embed both interface types correctly", func() {
			var clientSets flags.ClientSets

			// These should compile without error due to embedded interfaces
			_ = clientSets.Interface
			_ = clientSets.Client
		})
	})
})
