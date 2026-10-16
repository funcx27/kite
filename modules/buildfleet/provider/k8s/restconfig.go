package k8s

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NewRestConfig resolves the apiserver connection: an explicit kubeconfig
// path, else the pod's mounted ServiceAccount in-cluster, else the
// KUBECONFIG env var / ~/.kube/config.
func NewRestConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if c, err := rest.InClusterConfig(); err == nil {
		return c, nil
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), nil).ClientConfig()
}

// currentClients returns the apiserver clients in force, rebuilding them when
// the kubeconfig file changed since the last build: cluster rebuilds rotate
// the CA, and clients baked at provider registration would otherwise fail
// every call with x509 errors until a manual re-registration. A reload
// failure keeps the previous clients; the next call retries.
func (p *Provider) currentClients() (kubernetes.Interface, *rest.Config) {
	p.clientMu.Lock()
	defer p.clientMu.Unlock()
	if p.cfg.Kubeconfig == "" {
		return p.curClient, p.curRest
	}
	data, err := os.ReadFile(p.cfg.Kubeconfig)
	if err != nil {
		return p.curClient, p.curRest
	}
	sum := sha256.Sum256(data)
	if fp := hex.EncodeToString(sum[:]); fp != p.kubeFingerprint {
		kcfg, err := NewRestConfig(p.cfg.Kubeconfig)
		if err != nil {
			slog.Warn("k8s provider: reload kubeconfig after change", "err", err)
			return p.curClient, p.curRest
		}
		cs, err := kubernetes.NewForConfig(kcfg)
		if err != nil {
			slog.Warn("k8s provider: rebuild clientset after kubeconfig change", "err", err)
			return p.curClient, p.curRest
		}
		p.curClient, p.curRest, p.kubeFingerprint = cs, kcfg, fp
		slog.Info("k8s provider: rebuilt apiserver clients after kubeconfig change",
			"namespace", p.cfg.Namespace)
	}
	return p.curClient, p.curRest
}

func (p *Provider) clientset() kubernetes.Interface {
	cs, _ := p.currentClients()
	return cs
}

func (p *Provider) restConfig() *rest.Config {
	_, rc := p.currentClients()
	return rc
}
