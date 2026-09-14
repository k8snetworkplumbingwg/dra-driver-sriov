package framework

import (
	"context"
	"os"
	"strings"

	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	LabelMultus           = "Multus"
	LabelStandalone       = "Standalone"
	LabelVfio             = "Vfio"
	LabelExtendedResource = "ExtendedResource"
	LabelAlignment        = "Alignment"
	LabelResourcePolicy   = "ResourcePolicy"

	SriovCapableNodeLabelKey   = "feature.node.kubernetes.io/network-sriov.capable"
	SriovCapableNodeLabelValue = "true"
)

// SkipUnlessMultus skips when Multus is not installed or the driver is not in MULTUS mode.
func (c *Clients) SkipUnlessMultus(ctx context.Context) {
	if os.Getenv("E2E_SKIP_MULTUS") == "1" {
		skipTestf("E2E_SKIP_MULTUS=1")
	}
	if !c.hasMultus(ctx) {
		skipTestf("Multus not detected in cluster")
	}
	if mode := c.detectDriverMode(ctx); mode != "" && mode != "MULTUS" {
		skipTestf("driver configurationMode=%s (need MULTUS)", mode)
	}
}

// SkipUnlessStandalone skips demos that rely on the NRI attach path (ifName /
// netAttachDefName) when the driver is in MULTUS mode. In MULTUS mode NRI is
// disabled and secondary interfaces only appear via Multus pod annotations.
func (c *Clients) SkipUnlessStandalone(ctx context.Context) {
	if mode := c.detectDriverMode(ctx); mode == "MULTUS" {
		skipTestf("driver configurationMode=MULTUS (need STANDALONE / NRI)")
	}
}

// SkipUnlessAlignment skips unless explicitly enabled (needs gpu.example.com).
func (c *Clients) SkipUnlessAlignment(_ context.Context) {
	if os.Getenv("E2E_ENABLE_ALIGNMENT") != "1" {
		skipTestf("alignment e2e disabled by default; set E2E_ENABLE_ALIGNMENT=1 to run (requires gpu.example.com)")
	}
}

// SkipUnlessSriovCapableNode skips when no node has the SR-IOV capable label.
func (c *Clients) SkipUnlessSriovCapableNode(ctx context.Context) {
	nodes, err := c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: SriovCapableNodeLabelKey + "=" + SriovCapableNodeLabelValue,
	})
	Expect(err).NotTo(HaveOccurred())
	if len(nodes.Items) == 0 {
		skipTestf("no nodes with label %s=%s", SriovCapableNodeLabelKey, SriovCapableNodeLabelValue)
	}
}

// SkipIfNodeMissing skips when the node is absent.
func (c *Clients) SkipIfNodeMissing(ctx context.Context, wantNode string) {
	nodes, err := c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	Expect(err).NotTo(HaveOccurred())
	for _, n := range nodes.Items {
		if n.Name == wantNode {
			return
		}
	}
	skipTestf("node %s not found", wantNode)
}

func (c *Clients) hasMultus(ctx context.Context) bool {
	nss := []string{"kube-system", "multus", "default"}
	for _, ns := range nss {
		pods, err := c.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
			LabelSelector: "name=multus",
		})
		if err == nil && len(pods.Items) > 0 {
			return true
		}
		dss, err := c.Clientset.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}
		for _, ds := range dss.Items {
			if strings.Contains(strings.ToLower(ds.Name), "multus") {
				return true
			}
		}
	}
	return false
}

func (c *Clients) detectDriverMode(ctx context.Context) string {
	pods, err := c.Clientset.CoreV1().Pods(DriverNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: DriverLabel,
	})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}
	for _, p := range pods.Items {
		for _, ctn := range p.Spec.Containers {
			joined := strings.ToUpper(strings.Join(ctn.Args, " "))
			if strings.Contains(joined, "MULTUS") {
				return "MULTUS"
			}
			if strings.Contains(joined, "STANDALONE") {
				return "STANDALONE"
			}
			for _, env := range ctn.Env {
				if strings.EqualFold(env.Name, "CONFIGURATION_MODE") || strings.EqualFold(env.Name, "DRA_DRIVER_MODE") {
					return strings.ToUpper(env.Value)
				}
			}
		}
	}
	return "STANDALONE"
}
