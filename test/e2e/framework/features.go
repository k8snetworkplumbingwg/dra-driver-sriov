package framework

import (
	"context"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	LabelMultus           = "Multus"
	LabelVfio             = "Vfio"
	LabelExtendedResource = "ExtendedResource"
	LabelAlignment        = "Alignment"
	LabelResourcePolicy   = "ResourcePolicy"
)

// SkipUnlessMultus skips when Multus is not installed or the driver is not in MULTUS mode.
func (c *Clients) SkipUnlessMultus(ctx context.Context) {
	if os.Getenv("E2E_SKIP_MULTUS") == "1" {
		Skip("E2E_SKIP_MULTUS=1")
	}
	if !c.hasMultus(ctx) {
		Skip("Multus not detected in cluster")
	}
	if mode := c.detectDriverMode(ctx); mode != "" && mode != "MULTUS" {
		Skip("driver configurationMode=" + mode + " (need MULTUS)")
	}
}

// SkipUnlessAlignment skips unless explicitly enabled (needs gpu.example.com).
func (c *Clients) SkipUnlessAlignment(_ context.Context) {
	if os.Getenv("E2E_ENABLE_ALIGNMENT") != "1" {
		Skip("alignment e2e disabled by default; set E2E_ENABLE_ALIGNMENT=1 to run (requires gpu.example.com)")
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
	Skip("node " + wantNode + " not found")
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
