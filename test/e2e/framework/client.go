package framework

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

// Clients holds shared Kubernetes API clients for the e2e suite.
type Clients struct {
	Config    *rest.Config
	Clientset kubernetes.Interface
	Dynamic   dynamic.Interface
	Mapper    *restmapper.DeferredDiscoveryRESTMapper
}

// NewClients builds clients from KUBECONFIG (or the default kcli dra kubeconfig).
func NewClients() (*Clients, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		loadingRules.ExplicitPath = kc
	} else {
		home, err := os.UserHomeDir()
		if err == nil {
			candidate := filepath.Join(home, ".kcli", "clusters", "dra", "auth", "kubeconfig")
			if _, err := os.Stat(candidate); err == nil {
				loadingRules.ExplicitPath = candidate
			}
		}
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	config.QPS = 50
	config.Burst = 100

	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create clientset: %w", err)
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}
	dc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create discovery client: %w", err)
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))

	return &Clients{
		Config:    config,
		Clientset: cs,
		Dynamic:   dyn,
		Mapper:    mapper,
	}, nil
}
