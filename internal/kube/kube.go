// Package kube builds Kubernetes clients from kubeconfig or in-cluster config.
package kube

import (
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

// Dynamic returns a dynamic client and the context's default namespace.
// An empty kubeconfig uses $KUBECONFIG, ~/.kube/config, then in-cluster.
func Dynamic(kubeconfig, context string) (dynamic.Interface, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: context})
	rc, err := cc.ClientConfig()
	if err != nil {
		return nil, "", err
	}
	rc.UserAgent = "waveform"
	ns, _, err := cc.Namespace()
	if err != nil {
		return nil, "", err
	}
	dc, err := dynamic.NewForConfig(rc)
	return dc, ns, err
}
