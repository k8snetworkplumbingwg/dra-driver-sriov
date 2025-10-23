package podmanager_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"

	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/flags"
	"github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/podmanager"
	draTypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

var _ = Describe("PodManager", func() {
	var (
		pm       *podmanager.PodManager
		tempDir  string
		config   *draTypes.Config
		podUID   types.UID
		claimUID types.UID
		devices  draTypes.PreparedDevices
	)

	BeforeEach(func() {
		var err error
		tempDir, err = os.MkdirTemp("", "podmanager-test-*")
		Expect(err).NotTo(HaveOccurred())

		config = &draTypes.Config{
			Flags: &draTypes.Flags{
				KubeletPluginsDirectoryPath: tempDir,
			},
			K8sClient: flags.ClientSets{},
		}

		podUID = types.UID("test-pod-uid-12345")
		claimUID = types.UID("test-claim-uid-67890")

		devices = draTypes.PreparedDevices{
			{
				Device: drapbv1.Device{
					DeviceName: "test-device",
				},
				ClaimNamespacedName: kubeletplugin.NamespacedObject{
					UID: claimUID,
				},
				PciAddress: "0000:01:00.0",
				IfName:     "net1",
			},
			{
				Device: drapbv1.Device{
					DeviceName: "test-device-2",
				},
				ClaimNamespacedName: kubeletplugin.NamespacedObject{
					UID: claimUID,
				},
				PciAddress: "0000:01:00.1",
				IfName:     "net2",
			},
		}
	})

	AfterEach(func() {
		os.RemoveAll(tempDir)
	})

	Context("NewPodManager", func() {
		It("should create new pod manager successfully", func() {
			var err error
			pm, err = podmanager.NewPodManager(config)
			Expect(err).NotTo(HaveOccurred())
			Expect(pm).NotTo(BeNil())
		})

		It("should create checkpoint file on first run", func() {
			var err error
			pm, err = podmanager.NewPodManager(config)
			Expect(err).NotTo(HaveOccurred())

			// Verify checkpoint file exists in the driver directory
			driverDir := config.DriverPluginPath()
			checkpointPath := filepath.Join(driverDir, "checkpoint.json")
			_, err = os.Stat(checkpointPath)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should load existing checkpoint on subsequent runs", func() {
			// First, create a pod manager and add some data
			var err error
			pm, err = podmanager.NewPodManager(config)
			Expect(err).NotTo(HaveOccurred())

			err = pm.Set(podUID, claimUID, devices)
			Expect(err).NotTo(HaveOccurred())

			// Create a new pod manager from the same path - should load existing data
			pm2, err := podmanager.NewPodManager(config)
			Expect(err).NotTo(HaveOccurred())

			// Verify data was loaded
			loadedDevices, found := pm2.Get(podUID, claimUID)
			Expect(found).To(BeTrue())
			Expect(len(loadedDevices)).To(Equal(len(devices)))
			Expect(loadedDevices[0].PciAddress).To(Equal(devices[0].PciAddress))
		})

		It("should handle invalid checkpoint directory", func() {
			invalidConfig := &draTypes.Config{
				Flags: &draTypes.Flags{
					KubeletPluginsDirectoryPath: "/invalid/path/that/does/not/exist",
				},
			}

			_, err := podmanager.NewPodManager(invalidConfig)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unable to create checkpoint manager"))
		})
	})

	Context("Set and Get operations", func() {
		BeforeEach(func() {
			var err error
			pm, err = podmanager.NewPodManager(config)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should set and get devices for a pod/claim", func() {
			err := pm.Set(podUID, claimUID, devices)
			Expect(err).NotTo(HaveOccurred())

			retrievedDevices, found := pm.Get(podUID, claimUID)
			Expect(found).To(BeTrue())
			Expect(len(retrievedDevices)).To(Equal(2))
			Expect(retrievedDevices[0].PciAddress).To(Equal("0000:01:00.0"))
			Expect(retrievedDevices[1].PciAddress).To(Equal("0000:01:00.1"))
		})

		It("should return false for non-existent pod/claim", func() {
			_, found := pm.Get(types.UID("non-existent-pod"), types.UID("non-existent-claim"))
			Expect(found).To(BeFalse())
		})

		It("should return false for existing pod but non-existent claim", func() {
			err := pm.Set(podUID, claimUID, devices)
			Expect(err).NotTo(HaveOccurred())

			_, found := pm.Get(podUID, types.UID("non-existent-claim"))
			Expect(found).To(BeFalse())
		})

		It("should overwrite existing devices when setting again", func() {
			// Set initial devices
			err := pm.Set(podUID, claimUID, devices)
			Expect(err).NotTo(HaveOccurred())

			// Set new devices for the same pod/claim
			newDevices := draTypes.PreparedDevices{
				{
					Device: drapbv1.Device{
						DeviceName: "new-device",
					},
					PciAddress: "0000:02:00.0",
					IfName:     "net3",
				},
			}

			err = pm.Set(podUID, claimUID, newDevices)
			Expect(err).NotTo(HaveOccurred())

			// Verify new devices replaced old ones
			retrievedDevices, found := pm.Get(podUID, claimUID)
			Expect(found).To(BeTrue())
			Expect(len(retrievedDevices)).To(Equal(1))
			Expect(retrievedDevices[0].PciAddress).To(Equal("0000:02:00.0"))
		})

		It("should handle multiple claims for the same pod", func() {
			claim2UID := types.UID("test-claim-uid-99999")
			devices2 := draTypes.PreparedDevices{
				{
					Device: drapbv1.Device{
						DeviceName: "another-device",
					},
					PciAddress: "0000:02:00.0",
				},
			}

			// Set devices for two different claims under the same pod
			err := pm.Set(podUID, claimUID, devices)
			Expect(err).NotTo(HaveOccurred())

			err = pm.Set(podUID, claim2UID, devices2)
			Expect(err).NotTo(HaveOccurred())

			// Verify both claims exist
			devices1, found1 := pm.Get(podUID, claimUID)
			Expect(found1).To(BeTrue())
			Expect(len(devices1)).To(Equal(2))

			devices2Retrieved, found2 := pm.Get(podUID, claim2UID)
			Expect(found2).To(BeTrue())
			Expect(len(devices2Retrieved)).To(Equal(1))
			Expect(devices2Retrieved[0].PciAddress).To(Equal("0000:02:00.0"))
		})
	})

	Context("GetDevicesByPodUID", func() {
		BeforeEach(func() {
			var err error
			pm, err = podmanager.NewPodManager(config)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should get all devices for a pod with multiple claims", func() {
			claim2UID := types.UID("test-claim-uid-99999")
			devices2 := draTypes.PreparedDevices{
				{
					Device: drapbv1.Device{
						DeviceName: "another-device",
					},
					PciAddress: "0000:02:00.0",
				},
			}

			// Set devices for two claims
			err := pm.Set(podUID, claimUID, devices)
			Expect(err).NotTo(HaveOccurred())

			err = pm.Set(podUID, claim2UID, devices2)
			Expect(err).NotTo(HaveOccurred())

			// Get all devices for the pod
			allDevices, found := pm.GetDevicesByPodUID(podUID)
			Expect(found).To(BeTrue())
			Expect(len(allDevices)).To(Equal(3)) // 2 from first claim + 1 from second claim

			// Verify devices are from both claims
			pciAddresses := []string{}
			for _, device := range allDevices {
				pciAddresses = append(pciAddresses, device.PciAddress)
			}
			Expect(pciAddresses).To(ContainElement("0000:01:00.0"))
			Expect(pciAddresses).To(ContainElement("0000:01:00.1"))
			Expect(pciAddresses).To(ContainElement("0000:02:00.0"))
		})

		It("should return false for non-existent pod", func() {
			_, found := pm.GetDevicesByPodUID(types.UID("non-existent-pod"))
			Expect(found).To(BeFalse())
		})

		It("should handle empty pod (no claims)", func() {
			// This shouldn't happen in practice, but test edge case
			_, found := pm.GetDevicesByPodUID(podUID)
			Expect(found).To(BeFalse())
		})
	})

	Context("GetByClaim", func() {
		BeforeEach(func() {
			var err error
			pm, err = podmanager.NewPodManager(config)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should get devices by claim UID", func() {
			err := pm.Set(podUID, claimUID, devices)
			Expect(err).NotTo(HaveOccurred())

			claim := kubeletplugin.NamespacedObject{
				UID: claimUID,
			}

			retrievedDevices, found := pm.GetByClaim(claim)
			Expect(found).To(BeTrue())
			Expect(len(retrievedDevices)).To(Equal(2))
			Expect(retrievedDevices[0].PciAddress).To(Equal("0000:01:00.0"))
		})

		It("should return false for non-existent claim", func() {
			claim := kubeletplugin.NamespacedObject{
				UID: types.UID("non-existent-claim"),
			}

			_, found := pm.GetByClaim(claim)
			Expect(found).To(BeFalse())
		})

		It("should find claim across multiple pods", func() {
			pod2UID := types.UID("test-pod-uid-54321")

			// Add same claim to two different pods (this might not happen in practice, but test the logic)
			err := pm.Set(podUID, claimUID, devices)
			Expect(err).NotTo(HaveOccurred())

			err = pm.Set(pod2UID, claimUID, devices[:1]) // Only first device
			Expect(err).NotTo(HaveOccurred())

			claim := kubeletplugin.NamespacedObject{
				UID: claimUID,
			}

			// Should find the claim and return devices from the first pod that matches
			retrievedDevices, found := pm.GetByClaim(claim)
			Expect(found).To(BeTrue())
			Expect(len(retrievedDevices)).To(BeNumerically(">=", 1))
		})
	})

	Context("Delete operations", func() {
		BeforeEach(func() {
			var err error
			pm, err = podmanager.NewPodManager(config)
			Expect(err).NotTo(HaveOccurred())

			// Set up test data
			err = pm.Set(podUID, claimUID, devices)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should delete entire pod", func() {
			// Verify pod exists before deletion
			_, found := pm.GetDevicesByPodUID(podUID)
			Expect(found).To(BeTrue())

			// Delete pod
			err := pm.DeletePod(podUID)
			Expect(err).NotTo(HaveOccurred())

			// Verify pod no longer exists
			_, found = pm.GetDevicesByPodUID(podUID)
			Expect(found).To(BeFalse())

			// Verify specific claim also not found
			_, found = pm.Get(podUID, claimUID)
			Expect(found).To(BeFalse())
		})

		It("should delete by claim", func() {
			claim := kubeletplugin.NamespacedObject{
				UID: claimUID,
			}

			// Verify claim exists before deletion
			_, found := pm.GetByClaim(claim)
			Expect(found).To(BeTrue())

			// Delete claim
			err := pm.DeleteClaim(claim)
			Expect(err).NotTo(HaveOccurred())

			// Verify claim no longer exists
			_, found = pm.GetByClaim(claim)
			Expect(found).To(BeFalse())

			// Verify entire pod was deleted (current implementation deletes whole pod)
			_, found = pm.GetDevicesByPodUID(podUID)
			Expect(found).To(BeFalse())
		})

		It("should handle deleting non-existent pod", func() {
			err := pm.DeletePod(types.UID("non-existent-pod"))
			Expect(err).NotTo(HaveOccurred()) // Should not error

			// Verify existing data is still there
			_, found := pm.GetDevicesByPodUID(podUID)
			Expect(found).To(BeTrue())
		})

		It("should handle deleting non-existent claim", func() {
			nonExistentClaim := kubeletplugin.NamespacedObject{
				UID: types.UID("non-existent-claim"),
			}

			err := pm.DeleteClaim(nonExistentClaim)
			Expect(err).NotTo(HaveOccurred()) // Should not error

			// Verify existing data is still there
			_, found := pm.GetDevicesByPodUID(podUID)
			Expect(found).To(BeTrue())
		})
	})

	Context("Checkpoint synchronization", func() {
		BeforeEach(func() {
			var err error
			pm, err = podmanager.NewPodManager(config)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should persist data across operations", func() {
			// Add data
			err := pm.Set(podUID, claimUID, devices)
			Expect(err).NotTo(HaveOccurred())

			// Create new manager from same checkpoint
			pm2, err := podmanager.NewPodManager(config)
			Expect(err).NotTo(HaveOccurred())

			// Verify data persisted
			retrievedDevices, found := pm2.Get(podUID, claimUID)
			Expect(found).To(BeTrue())
			Expect(len(retrievedDevices)).To(Equal(2))

			// Delete data in second manager
			err = pm2.DeletePod(podUID)
			Expect(err).NotTo(HaveOccurred())

			// Create third manager and verify deletion persisted
			pm3, err := podmanager.NewPodManager(config)
			Expect(err).NotTo(HaveOccurred())

			_, found = pm3.Get(podUID, claimUID)
			Expect(found).To(BeFalse())
		})

		It("should handle checkpoint sync errors gracefully", func() {
			// This is hard to test without mocking the checkpoint manager
			// For now, we'll test that normal operations work
			err := pm.Set(podUID, claimUID, devices)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("Concurrent access", func() {
		BeforeEach(func() {
			var err error
			pm, err = podmanager.NewPodManager(config)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle concurrent reads and writes", func() {
			// This is a basic concurrency test
			// In practice, more sophisticated testing would be needed
			done := make(chan bool)

			// Writer goroutine
			go func() {
				defer GinkgoRecover()
				for i := 0; i < 10; i++ {
					testPodUID := types.UID("test-pod-" + string(rune(i)))
					testClaimUID := types.UID("test-claim-" + string(rune(i)))
					err := pm.Set(testPodUID, testClaimUID, devices[:1])
					Expect(err).NotTo(HaveOccurred())
				}
				done <- true
			}()

			// Reader goroutine
			go func() {
				defer GinkgoRecover()
				for i := 0; i < 10; i++ {
					testPodUID := types.UID("test-pod-" + string(rune(i)))
					testClaimUID := types.UID("test-claim-" + string(rune(i)))
					_, _ = pm.Get(testPodUID, testClaimUID) // Don't care about result, just that it doesn't panic
				}
				done <- true
			}()

			// Wait for both goroutines
			<-done
			<-done
		})
	})

	Context("Edge cases", func() {
		BeforeEach(func() {
			var err error
			pm, err = podmanager.NewPodManager(config)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle empty devices slice", func() {
			emptyDevices := draTypes.PreparedDevices{}

			err := pm.Set(podUID, claimUID, emptyDevices)
			Expect(err).NotTo(HaveOccurred())

			retrievedDevices, found := pm.Get(podUID, claimUID)
			Expect(found).To(BeTrue())
			Expect(len(retrievedDevices)).To(Equal(0))
		})

		It("should handle nil devices slice", func() {
			var nilDevices draTypes.PreparedDevices

			err := pm.Set(podUID, claimUID, nilDevices)
			Expect(err).NotTo(HaveOccurred())

			retrievedDevices, found := pm.Get(podUID, claimUID)
			Expect(found).To(BeTrue())
			Expect(len(retrievedDevices)).To(Equal(0))
		})

		It("should handle empty string UIDs", func() {
			err := pm.Set(types.UID(""), types.UID(""), devices)
			Expect(err).NotTo(HaveOccurred())

			retrievedDevices, found := pm.Get(types.UID(""), types.UID(""))
			Expect(found).To(BeTrue())
			Expect(len(retrievedDevices)).To(Equal(2))
		})
	})
})
