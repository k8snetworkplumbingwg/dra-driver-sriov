"""Two live POC tests. Run with KUBECONFIG set; leaves workloads for inspection."""

import ipaddress
import json
from pathlib import Path
import subprocess
import unittest


NAMESPACE = "dra-multus-poc"
DRIVER = "sriovnetwork.k8snetworkplumbingwg.io"
ROOT = "resource.kubernetes.io/pcieRoot"
FIXTURES = Path(__file__).resolve().parents[2] / "demo/multus-auto-attach"


def kubectl(*args):
    return subprocess.check_output(["kubectl", *map(str, args)], text=True)


def get(kind, name=None, namespace=NAMESPACE):
    args = ["get", kind]
    if name:
        args.append(name)
    if namespace:
        args.extend(["-n", namespace])
    return json.loads(kubectl(*args, "-o", "json"))


class MultusAutoAttach(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        kubectl("rollout", "status", "-n", "kube-system", "ds/kube-multus-ds", "--timeout=180s")
        kubectl("rollout", "status", "-n", "dra-driver-sriov", "ds/dra-driver-sriov-dra-driver-sriov-chart-kubeletplugin", "--timeout=180s")

    def apply(self, filename):
        path = FIXTURES / filename
        output = kubectl("create", "--dry-run=client", "-f", path, "-o", "json")
        objects = []
        while output.strip():
            item, end = json.JSONDecoder().raw_decode(output.lstrip())
            objects.append(item)
            output = output.lstrip()[end:]
        for item in objects:
            annotations = item["metadata"].get("annotations", {})
            self.assertNotIn("k8s.v1.cni.cncf.io/networks", annotations)
            self.assertNotIn("k8s.v1.cni.cncf.io/resourceName", annotations)
            if item["kind"] == "NetworkAttachmentDefinition":
                self.assertNotIn("deviceID", json.loads(item["spec"]["config"]))
        kubectl("apply", "-f", path)

    def check_workload(self, name, count):
        kubectl("wait", "-n", NAMESPACE, "--for=condition=Ready", f"pod/{name}", "--timeout=180s")
        pod = get("pod", name)
        claim_name = pod["status"]["resourceClaimStatuses"][0]["resourceClaimName"]
        claim = get("resourceclaim", claim_name)
        inventory = {}
        for resource_slice in get("resourceslices", namespace=None)["items"]:
            spec = resource_slice["spec"]
            for device in spec["devices"]:
                inventory[(spec["driver"], spec["pool"]["name"], device["name"])] = device
        allocations = claim["status"]["allocation"]["devices"]["results"]
        by_request = {result["request"]: inventory[(result["driver"], result["pool"], result["device"])] for result in allocations}
        sriov = [inventory[(r["driver"], r["pool"], r["device"])] for r in allocations if r["driver"] == DRIVER]
        expected_pci = {device["attributes"][f"{DRIVER}/pciAddress"]["string"] for device in sriov}
        selections = json.loads(pod["metadata"]["annotations"]["k8s.v1.cni.cncf.io/networks"])
        self.assertEqual(len(selections), count)
        status = json.loads(pod["metadata"]["annotations"]["k8s.v1.cni.cncf.io/network-status"])
        actual_pci = set()
        rail_names = set()
        for selection in selections:
            shadow = get("network-attachment-definition", selection["name"])
            self.assertNotIn("k8s.v1.cni.cncf.io/resourceName", shadow["metadata"].get("annotations", {}))
            self.assertEqual(shadow["metadata"]["ownerReferences"][0]["uid"], pod["metadata"]["uid"])
            config = json.loads(shadow["spec"]["config"])
            actual_pci.add(config["deviceID"])
            rail_names.add(config["name"])
            attachment = next(entry for entry in status if entry["name"] == f"{NAMESPACE}/{shadow['metadata']['name']}")
            interface = attachment["interface"]
            device_path = kubectl("exec", "-n", NAMESPACE, name, "--", "readlink", "-f", f"/sys/class/net/{interface}/device")
            self.assertEqual(Path(device_path.strip()).name, config["deviceID"])
            addresses = json.loads(kubectl("exec", "-n", NAMESPACE, name, "--", "ip", "-j", "-4", "addr", "show", "dev", interface))
            subnet = ipaddress.ip_network(config["ipam"]["subnet"])
            self.assertTrue(any(ipaddress.ip_address(addr["local"]) in subnet for addr in addresses[0]["addr_info"]))
            if count == 2:
                device = next(device for device in sriov if device["attributes"][f"{DRIVER}/pciAddress"]["string"] == config["deviceID"])
                template = device["attributes"][f"{DRIVER}/netAttachDefName"]["string"]
            else:
                template = "single-network"
            source = get("network-attachment-definition", template)
            source_config = json.loads(source["spec"]["config"])
            self.assertNotIn("deviceID", source_config)
            self.assertEqual(config["name"], source_config.get("name", template))
            self.assertEqual(config["ipam"], source_config["ipam"])
            print(f"{name}: {interface} PCI={config['deviceID']} network={config['name']} IPs={attachment.get('ips')}", flush=True)
        self.assertEqual(actual_pci, expected_pci)
        return by_request, rail_names

    def test_01_annotation_free_single_vf(self):
        self.apply("single-vf.yaml")
        self.check_workload("workload1", 1)

    def test_02_gpu_nic_pairs_and_rails(self):
        self.apply("gpu-nic-pairs.yaml")
        devices, rails = self.check_workload("workload2", 2)
        for pair in range(2):
            gpu = devices[f"gpu-{pair}"]["attributes"][ROOT]["string"]
            nic = devices[f"nic-{pair}"]["attributes"][ROOT]["string"]
            self.assertEqual(gpu, nic)
            print(f"pair {pair}: GPU and NIC share {gpu}", flush=True)
        self.assertNotEqual(devices["gpu-0"]["attributes"][ROOT]["string"], devices["gpu-1"]["attributes"][ROOT]["string"])
        self.assertEqual(rails, {"poc-rail-a", "poc-rail-b"})


if __name__ == "__main__":
    unittest.main(verbosity=2)
