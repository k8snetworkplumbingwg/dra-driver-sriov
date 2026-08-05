package framework

import (
	"context"
	"fmt"
	"io"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DebugDump writes diagnostic information to stderr.
func (c *Clients) DebugDump(ctx context.Context, namespaces ...string) {
	w := io.Writer(os.Stderr)
	fmt.Fprintln(w, "=== e2e failure dump ===")

	nodes, err := c.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Fprintf(w, "nodes: %v\n", err)
	} else {
		fmt.Fprintln(w, "## Nodes")
		for _, n := range nodes.Items {
			fmt.Fprintf(w, "  %s Ready=%v\n", n.Name, isNodeReady(n))
		}
	}

	fmt.Fprintln(w, "## DRA driver pods")
	pods, err := c.Clientset.CoreV1().Pods(DriverNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: DriverLabel,
	})
	if err != nil {
		fmt.Fprintf(w, "  %v\n", err)
	} else {
		tail := int64(100)
		for _, p := range pods.Items {
			fmt.Fprintf(w, "  %s/%s phase=%s\n", p.Namespace, p.Name, p.Status.Phase)
			logs, err := c.Clientset.CoreV1().Pods(p.Namespace).GetLogs(p.Name, &corev1.PodLogOptions{
				TailLines: &tail,
			}).Do(ctx).Raw()
			if err != nil {
				fmt.Fprintf(w, "  logs: %v\n", err)
			} else {
				fmt.Fprintf(w, "--- logs %s ---\n%s\n", p.Name, string(logs))
			}
		}
	}

	fmt.Fprintln(w, "## ResourceSlices")
	slices, err := c.Dynamic.Resource(schemaGroupVersionResource("resourceslices")).List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Fprintf(w, "  %v\n", err)
	} else {
		for _, s := range slices.Items {
			devices, _, _ := unstructured.NestedSlice(s.Object, "spec", "devices")
			fmt.Fprintf(w, "  %s devices=%d\n", s.GetName(), len(devices))
		}
	}

	for _, ns := range namespaces {
		if ns == "" {
			continue
		}
		fmt.Fprintf(w, "## Namespace %s pods\n", ns)
		nsPods, err := c.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			fmt.Fprintf(w, "  %v\n", err)
			continue
		}
		for _, p := range nsPods.Items {
			fmt.Fprintf(w, "  %s phase=%s reason=%s message=%s\n",
				p.Name, p.Status.Phase, p.Status.Reason, p.Status.Message)
		}

		fmt.Fprintf(w, "## Namespace %s ResourceClaims\n", ns)
		claims, err := c.Dynamic.Resource(ResourceClaimGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			fmt.Fprintf(w, "  %v\n", err)
		} else {
			for _, claim := range claims.Items {
				fmt.Fprintf(w, "  %s\n", claim.GetName())
			}
		}

		fmt.Fprintf(w, "## Namespace %s events\n", ns)
		events, err := c.Clientset.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			fmt.Fprintf(w, "  %v\n", err)
		} else {
			for _, e := range events.Items {
				fmt.Fprintf(w, "  %s %s %s: %s\n", e.LastTimestamp, e.Type, e.Reason, e.Message)
			}
		}
	}
}

func isNodeReady(n corev1.Node) bool {
	for _, cond := range n.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}
