package framework

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// ExecInPod runs a command in a pod container and returns combined stdout+stderr.
func (c *Clients) ExecInPod(ctx context.Context, namespace, pod, container string, command ...string) (string, error) {
	req := c.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(c.Config, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	out := strings.TrimSpace(stdout.String())
	errOut := strings.TrimSpace(stderr.String())
	if err != nil {
		return out, fmt.Errorf("exec %v in %s/%s: %w (stderr: %s)", command, namespace, pod, err, errOut)
	}
	if errOut != "" && out == "" {
		return errOut, nil
	}
	if errOut != "" {
		return out + "\n" + errOut, nil
	}
	return out, nil
}

// ExpectInterfaceHasAddress asserts ifName exists in the pod and has an IP address.
func (c *Clients) ExpectInterfaceHasAddress(ctx context.Context, namespace, pod, container, ifName string) {
	var out string
	var err error
	out, err = c.ExecInPod(ctx, namespace, pod, container, "ip", "addr", "show", ifName)
	Expect(err).NotTo(HaveOccurred(), "ip addr show %s", ifName)
	Expect(out).To(ContainSubstring(ifName))
	Expect(out).To(Or(ContainSubstring("inet "), ContainSubstring("inet6 ")),
		"interface %s has no address:\n%s", ifName, out)
}

// ExpectPathExists asserts that path exists inside the pod.
func (c *Clients) ExpectPathExists(ctx context.Context, namespace, pod, container, path string) {
	_, err := c.ExecInPod(ctx, namespace, pod, container, "test", "-e", path)
	Expect(err).NotTo(HaveOccurred(), "path %s should exist in %s/%s", path, namespace, pod)
}
