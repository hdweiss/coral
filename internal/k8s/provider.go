package k8s

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/disk"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/openapi"
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
	// OpenAPI returns the cluster's OpenAPI v3 client, or an error when the
	// cluster has none (demo mode).
	OpenAPI(context string) (openapi.Client, error)
	// Logs streams a container's log, each line prefixed with its RFC3339Nano
	// timestamp. Closing the reader or cancelling ctx ends the stream.
	Logs(ctx context.Context, context string, req LogRequest) (io.ReadCloser, error)
}

type kubeProvider struct {
	rules *clientcmd.ClientConfigLoadingRules
	raw   *clientcmdapi.Config
	opts  KubeOptions

	mu      sync.Mutex
	configs map[string]*rest.Config
	clients map[string]dynamic.Interface
	openapi map[string]openapi.Client
	typed   map[string]kubernetes.Interface
}

// KubeOptions configure the clients of real clusters.
type KubeOptions struct {
	Kubeconfig string  // "" loads $KUBECONFIG or ~/.kube/config
	Trace      *Tracer // logs every request when set
	// CacheDir keeps OpenAPI documents on disk, per context (see
	// CacheDir), like kubectl's ~/.kube/cache. "" keeps them in memory.
	CacheDir string
}

// NewKubeProvider loads the kubeconfig.
func NewKubeProvider(opts KubeOptions) (Provider, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.Kubeconfig != "" {
		rules.ExplicitPath = opts.Kubeconfig
	}
	raw, err := rules.Load()
	if err != nil {
		return nil, err
	}
	if len(raw.Contexts) == 0 {
		return nil, errors.New("no contexts found in kubeconfig (try --demo)")
	}
	return &kubeProvider{
		rules: rules, raw: raw, opts: opts,
		configs: map[string]*rest.Config{},
		clients: map[string]dynamic.Interface{},
		openapi: map[string]openapi.Client{},
		typed:   map[string]kubernetes.Interface{},
	}, nil
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

// config returns the REST config of a context. p.mu must be held.
func (p *kubeProvider) config(context string) (*rest.Config, error) {
	if rc, ok := p.configs[context]; ok {
		return rc, nil
	}
	cc := clientcmd.NewNonInteractiveClientConfig(*p.raw, context, &clientcmd.ConfigOverrides{}, p.rules)
	rc, err := cc.ClientConfig()
	if err != nil {
		return nil, err
	}
	rc.QPS, rc.Burst = 50, 100
	// Warnings would be printed on top of the TUI.
	rc.WarningHandler = rest.NoWarnings{}
	if p.opts.Trace != nil {
		rc.Wrap(p.opts.Trace.WrapTransport)
	}
	p.configs[context] = rc
	return rc, nil
}

func (p *kubeProvider) Client(context string) (dynamic.Interface, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[context]; ok {
		return c, nil
	}
	rc, err := p.config(context)
	if err != nil {
		return nil, err
	}
	c, err := dynamic.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	p.clients[context] = c
	return c, nil
}

func (p *kubeProvider) OpenAPI(context string) (openapi.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.openapi[context]; ok {
		return c, nil
	}
	rc, err := p.config(context)
	if err != nil {
		return nil, err
	}
	var dc discovery.DiscoveryInterface
	if p.opts.CacheDir != "" {
		// The documents' paths carry a hash and the server marks them
		// immutable, so the HTTP cache serves them from disk until the
		// index (revalidated by ETag) points elsewhere.
		dir := CacheDir(p.opts.CacheDir, context)
		dc, err = disk.NewCachedDiscoveryClientForConfig(rc, filepath.Join(dir, "discovery"), filepath.Join(dir, "http"), 6*time.Hour)
	} else {
		dc, err = discovery.NewDiscoveryClientForConfig(rc)
	}
	if err != nil {
		return nil, err
	}
	c := dc.OpenAPIV3()
	p.openapi[context] = c
	return c, nil
}

func (p *kubeProvider) clientset(context string) (kubernetes.Interface, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.typed[context]; ok {
		return c, nil
	}
	rc, err := p.config(context)
	if err != nil {
		return nil, err
	}
	c, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	p.typed[context] = c
	return c, nil
}

func (p *kubeProvider) Logs(ctx context.Context, context string, req LogRequest) (io.ReadCloser, error) {
	c, err := p.clientset(context)
	if err != nil {
		return nil, err
	}
	return c.CoreV1().Pods(req.Namespace).GetLogs(req.Pod, req.options()).Stream(ctx)
}

// CacheDir is a context's directory under the cache directory base. Context
// names may hold characters that paths can't, such as the slashes and colons
// of EKS ARNs; those become _, plus a hash so that names stay apart.
func CacheDir(base, context string) string {
	safe := []byte(context)
	changed := false
	for i, c := range safe {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_') || i == 0 && c == '.' {
			safe[i], changed = '_', true
		}
	}
	name := string(safe)
	if changed {
		name += "-" + hash(context, 8)
	}
	return filepath.Join(base, name)
}
