package k8s

import (
	"errors"
	"sort"
	"sync"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Provider hands out cluster clients per kubeconfig context.
type Provider interface {
	Contexts() []string
	Current() string
	DefaultNamespace(context string) string
	Client(context string) (dynamic.Interface, error)
}

type kubeProvider struct {
	rules *clientcmd.ClientConfigLoadingRules
	raw   *clientcmdapi.Config

	mu      sync.Mutex
	clients map[string]dynamic.Interface
}

// NewKubeProvider loads the kubeconfig from path, or from the default
// locations ($KUBECONFIG, ~/.kube/config) when path is empty.
func NewKubeProvider(path string) (Provider, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.ExplicitPath = path
	}
	raw, err := rules.Load()
	if err != nil {
		return nil, err
	}
	if len(raw.Contexts) == 0 {
		return nil, errors.New("no contexts found in kubeconfig (try --demo)")
	}
	return &kubeProvider{rules: rules, raw: raw, clients: map[string]dynamic.Interface{}}, nil
}

func (p *kubeProvider) Contexts() []string {
	out := make([]string, 0, len(p.raw.Contexts))
	for name := range p.raw.Contexts {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (p *kubeProvider) Current() string {
	if p.raw.CurrentContext != "" {
		return p.raw.CurrentContext
	}
	return p.Contexts()[0]
}

func (p *kubeProvider) DefaultNamespace(context string) string {
	if c, ok := p.raw.Contexts[context]; ok && c.Namespace != "" {
		return c.Namespace
	}
	return "default"
}

func (p *kubeProvider) Client(context string) (dynamic.Interface, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[context]; ok {
		return c, nil
	}
	cc := clientcmd.NewNonInteractiveClientConfig(*p.raw, context, &clientcmd.ConfigOverrides{}, p.rules)
	rc, err := cc.ClientConfig()
	if err != nil {
		return nil, err
	}
	rc.QPS, rc.Burst = 50, 100
	// Warnings would be printed on top of the TUI.
	rc.WarningHandler = rest.NoWarnings{}
	c, err := dynamic.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	p.clients[context] = c
	return c, nil
}
