package types_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"

	draTypes "github.com/k8snetworkplumbingwg/dra-driver-sriov/pkg/types"
)

var _ = Describe("Types", func() {
	Context("AddDeviceIDToNetConf", func() {
		It("should add deviceID to valid JSON config", func() {
			originalConfig := `{"type": "sriov", "name": "mynet"}`
			deviceID := "0000:01:00.0"

			result, err := draTypes.AddDeviceIDToNetConf(originalConfig, deviceID)
			Expect(err).NotTo(HaveOccurred())

			// Parse result to verify deviceID was added
			var config map[string]interface{}
			err = json.Unmarshal([]byte(result), &config)
			Expect(err).NotTo(HaveOccurred())
			Expect(config["deviceID"]).To(Equal(deviceID))
			Expect(config["type"]).To(Equal("sriov"))
			Expect(config["name"]).To(Equal("mynet"))
		})

		It("should replace existing deviceID in config", func() {
			originalConfig := `{"type": "sriov", "deviceID": "old-device", "name": "mynet"}`
			deviceID := "0000:01:00.0"

			result, err := draTypes.AddDeviceIDToNetConf(originalConfig, deviceID)
			Expect(err).NotTo(HaveOccurred())

			// Parse result to verify deviceID was replaced
			var config map[string]interface{}
			err = json.Unmarshal([]byte(result), &config)
			Expect(err).NotTo(HaveOccurred())
			Expect(config["deviceID"]).To(Equal(deviceID))
			Expect(config["type"]).To(Equal("sriov"))
			Expect(config["name"]).To(Equal("mynet"))
		})

		It("should handle empty JSON object", func() {
			originalConfig := `{}`
			deviceID := "0000:01:00.0"

			result, err := draTypes.AddDeviceIDToNetConf(originalConfig, deviceID)
			Expect(err).NotTo(HaveOccurred())

			// Parse result to verify deviceID was added
			var config map[string]interface{}
			err = json.Unmarshal([]byte(result), &config)
			Expect(err).NotTo(HaveOccurred())
			Expect(config["deviceID"]).To(Equal(deviceID))
		})

		It("should handle complex nested configuration", func() {
			originalConfig := `{
				"type": "sriov",
				"name": "mynet",
				"ipam": {
					"type": "static",
					"addresses": ["192.168.1.100/24"]
				},
				"capabilities": {
					"ips": true
				}
			}`
			deviceID := "0000:01:00.0"

			result, err := draTypes.AddDeviceIDToNetConf(originalConfig, deviceID)
			Expect(err).NotTo(HaveOccurred())

			// Parse result to verify deviceID was added and other fields preserved
			var config map[string]interface{}
			err = json.Unmarshal([]byte(result), &config)
			Expect(err).NotTo(HaveOccurred())
			Expect(config["deviceID"]).To(Equal(deviceID))
			Expect(config["type"]).To(Equal("sriov"))
			Expect(config["name"]).To(Equal("mynet"))

			// Check nested structures are preserved
			ipam, exists := config["ipam"].(map[string]interface{})
			Expect(exists).To(BeTrue())
			Expect(ipam["type"]).To(Equal("static"))

			capabilities, exists := config["capabilities"].(map[string]interface{})
			Expect(exists).To(BeTrue())
			Expect(capabilities["ips"]).To(BeTrue())
		})

		It("should return error for invalid JSON", func() {
			originalConfig := `invalid json`
			deviceID := "0000:01:00.0"

			_, err := draTypes.AddDeviceIDToNetConf(originalConfig, deviceID)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to unmarshal existing config"))
		})

		It("should handle empty deviceID", func() {
			originalConfig := `{"type": "sriov", "name": "mynet"}`
			deviceID := ""

			result, err := draTypes.AddDeviceIDToNetConf(originalConfig, deviceID)
			Expect(err).NotTo(HaveOccurred())

			// Parse result to verify empty deviceID was added
			var config map[string]interface{}
			err = json.Unmarshal([]byte(result), &config)
			Expect(err).NotTo(HaveOccurred())
			Expect(config["deviceID"]).To(Equal(""))
		})
	})

	Context("Checkpoint operations", func() {
		var checkpoint *draTypes.Checkpoint

		BeforeEach(func() {
			checkpoint = draTypes.NewCheckpoint()
		})

		It("should create new checkpoint with correct structure", func() {
			Expect(checkpoint).NotTo(BeNil())
			Expect(uint64(checkpoint.Checksum)).To(Equal(uint64(0)))
			Expect(checkpoint.V1).NotTo(BeNil())
			Expect(checkpoint.V1.PreparedClaimsByPodUID).NotTo(BeNil())
			Expect(len(checkpoint.V1.PreparedClaimsByPodUID)).To(Equal(0))
		})

		It("should marshal and unmarshal checkpoint correctly", func() {
			// Add some test data
			podUID := types.UID("test-pod-uid")
			claimUID := types.UID("test-claim-uid")

			checkpoint.V1.PreparedClaimsByPodUID[podUID] = make(draTypes.PreparedDevicesByClaimID)
			checkpoint.V1.PreparedClaimsByPodUID[podUID][claimUID] = draTypes.PreparedDevices{}

			// Marshal
			data, err := checkpoint.MarshalCheckpoint()
			Expect(err).NotTo(HaveOccurred())
			Expect(len(data)).To(BeNumerically(">", 0))

			// Unmarshal to new checkpoint
			newCheckpoint := draTypes.NewCheckpoint()
			err = newCheckpoint.UnmarshalCheckpoint(data)
			Expect(err).NotTo(HaveOccurred())

			// Verify data is preserved
			Expect(newCheckpoint.V1.PreparedClaimsByPodUID).To(HaveKey(podUID))
			Expect(newCheckpoint.V1.PreparedClaimsByPodUID[podUID]).To(HaveKey(claimUID))
		})

		It("should verify checksum correctly", func() {
			// Add some test data
			podUID := types.UID("test-pod-uid")
			claimUID := types.UID("test-claim-uid")

			checkpoint.V1.PreparedClaimsByPodUID[podUID] = make(draTypes.PreparedDevicesByClaimID)
			checkpoint.V1.PreparedClaimsByPodUID[podUID][claimUID] = draTypes.PreparedDevices{}

			// Marshal to calculate checksum
			data, err := checkpoint.MarshalCheckpoint()
			Expect(err).NotTo(HaveOccurred())

			// Unmarshal and verify checksum
			verifyCheckpoint := &draTypes.Checkpoint{}
			err = verifyCheckpoint.UnmarshalCheckpoint(data)
			Expect(err).NotTo(HaveOccurred())

			err = verifyCheckpoint.VerifyChecksum()
			Expect(err).NotTo(HaveOccurred())
		})

		It("should detect corrupted checksum", func() {
			// Marshal with correct checksum
			data, err := checkpoint.MarshalCheckpoint()
			Expect(err).NotTo(HaveOccurred())

			// Unmarshal
			corruptCheckpoint := &draTypes.Checkpoint{}
			err = corruptCheckpoint.UnmarshalCheckpoint(data)
			Expect(err).NotTo(HaveOccurred())

			// Corrupt the data by modifying it
			if corruptCheckpoint.V1.PreparedClaimsByPodUID == nil {
				corruptCheckpoint.V1.PreparedClaimsByPodUID = make(draTypes.PreparedClaimsByPodUID)
			}
			corruptCheckpoint.V1.PreparedClaimsByPodUID[types.UID("corrupt-data")] = make(draTypes.PreparedDevicesByClaimID)

			// Verify should fail
			err = corruptCheckpoint.VerifyChecksum()
			Expect(err).To(HaveOccurred())
		})

		It("should handle empty checkpoint marshal/unmarshal", func() {
			data, err := checkpoint.MarshalCheckpoint()
			Expect(err).NotTo(HaveOccurred())

			newCheckpoint := &draTypes.Checkpoint{}
			err = newCheckpoint.UnmarshalCheckpoint(data)
			Expect(err).NotTo(HaveOccurred())

			err = newCheckpoint.VerifyChecksum()
			Expect(err).NotTo(HaveOccurred())

			// Verify empty state is preserved
			Expect(len(newCheckpoint.V1.PreparedClaimsByPodUID)).To(Equal(0))
		})

		It("should handle invalid JSON in unmarshal", func() {
			invalidJSON := []byte("invalid json")

			err := checkpoint.UnmarshalCheckpoint(invalidJSON)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("Type definitions", func() {
		It("should define correct type aliases", func() {
			// Test that we can create instances of all type aliases
			allocatable := make(draTypes.AllocatableDevices)
			Expect(allocatable).NotTo(BeNil())

			var prepared draTypes.PreparedDevices
			Expect(prepared).To(BeEmpty())

			preparedByClaimID := make(draTypes.PreparedDevicesByClaimID)
			Expect(preparedByClaimID).NotTo(BeNil())

			preparedByPodUID := make(draTypes.PreparedClaimsByPodUID)
			Expect(preparedByPodUID).NotTo(BeNil())

			var networkDataList draTypes.NetworkDataChanStructList
			Expect(networkDataList).To(BeEmpty())
		})

		It("should allow proper usage of NetworkDataChanStruct", func() {
			networkData := &draTypes.NetworkDataChanStruct{
				PreparedDevice:    nil, // Would be actual PreparedDevice in real usage
				NetworkDeviceData: nil, // Would be actual NetworkDeviceData in real usage
			}
			Expect(networkData).NotTo(BeNil())

			// Test that it can be added to the list
			var networkDataList draTypes.NetworkDataChanStructList
			networkDataList = append(networkDataList, networkData)
			Expect(len(networkDataList)).To(Equal(1))
		})
	})
})
