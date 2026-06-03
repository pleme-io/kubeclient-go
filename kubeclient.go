// Package kubeclient is the fleet's one typed client-go helper — so no tool
// re-derives kubeconfig loading, exec-credential wiring (GKE / EKS, no
// temporary service account), pod exec / log streaming / file copy, or an
// ExternalSecret informer keyed by remote key.
//
// client-go is a weight-bearing dependency, so this whole library is the
// import-gated leaf (Law 6): a consumer that does not touch a Kubernetes API
// does not import it. The constructor follows the §3.5 canonical
// New(required…, opts…) / FromConfig(cfg) shapes; all runtime knobs live in a
// typed, yaml-tagged Config loaded once at main through shikumi-go.
//
// WORLDS-SEPARATE: this is a PUBLIC, generic primitive. It never names or
// imports akeyless. The ExternalSecret informer is keyed by the generic
// External-Secrets-Operator CRD's remoteRef.key — any SecretStore backend.
package kubeclient

import (
	"errors"
	"fmt"

	k8sauthconfig "github.com/pleme-io/k8sauthconfig-go"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Source selects where the rest.Config comes from.
type Source int

const (
	// SourceAuto tries in-cluster first, then the default kubeconfig loading
	// rules (KUBECONFIG / ~/.kube/config). This is the default.
	SourceAuto Source = iota
	// SourceInCluster uses the pod's mounted service-account token + the
	// in-cluster API server. No kubeconfig, no exec plugin.
	SourceInCluster
	// SourceKubeconfig loads from a kubeconfig file, honouring any `exec:`
	// credential plugin (gke-gcloud-auth-plugin, `aws eks get-token`, …) — the
	// no-temp-SA GKE / EKS path.
	SourceKubeconfig
	// SourceAuthConfig builds a rest.Config from an explicit k8sauthconfig.Config
	// (host + CA + bearer token), bypassing kubeconfig entirely.
	SourceAuthConfig
)

// Config is the typed, yaml-tagged runtime surface, loaded once at main through
// shikumi-go and handed to FromConfig. Zero value = SourceAuto.
type Config struct {
	// Source selects the rest.Config origin. Encoded as a string in yaml.
	Source string `yaml:"source" json:"source,omitempty"`
	// KubeconfigPath is the kubeconfig file (SourceKubeconfig). Empty = default
	// loading rules (KUBECONFIG env, then ~/.kube/config).
	KubeconfigPath string `yaml:"kubeconfigPath" json:"kubeconfigPath,omitempty"`
	// Context overrides the kubeconfig current-context (SourceKubeconfig).
	Context string `yaml:"context" json:"context,omitempty"`
	// Auth is the explicit cluster-auth tuple used by SourceAuthConfig. The
	// shared k8sauthconfig domain type — embedded, not redeclared.
	Auth k8sauthconfig.Config `yaml:"auth" json:"auth,omitempty"`
	// BearerToken is the token presented to the API server under SourceAuthConfig.
	// A secret — redacted in every textual surface.
	BearerToken k8sauthconfig.Secret `yaml:"bearerToken" json:"bearerToken,omitempty"`
	// QPS / Burst tune the client-side rate limiter (0 = client-go defaults).
	QPS   float32 `yaml:"qps" json:"qps,omitempty"`
	Burst int     `yaml:"burst" json:"burst,omitempty"`
}

// Client is the typed bundle of the resolved rest.Config plus the constructed
// typed + dynamic clients. The dynamic client backs the ExternalSecret informer
// (CRDs are not in the typed scheme).
type Client struct {
	Config    *rest.Config
	Clientset kubernetes.Interface
	Dynamic   dynamic.Interface
}

// Option configures Client construction.
type Option func(*Config)

// WithKubeconfig sets the kubeconfig path and selects SourceKubeconfig.
func WithKubeconfig(path string) Option {
	return func(c *Config) { c.KubeconfigPath = path; c.Source = "kubeconfig" }
}

// WithContext overrides the kubeconfig current-context.
func WithContext(name string) Option { return func(c *Config) { c.Context = name } }

// WithInCluster selects SourceInCluster.
func WithInCluster() Option { return func(c *Config) { c.Source = "in-cluster" } }

// WithAuthConfig selects SourceAuthConfig with an explicit auth tuple + token.
func WithAuthConfig(auth k8sauthconfig.Config, bearerToken string) Option {
	return func(c *Config) {
		c.Source = "auth-config"
		c.Auth = auth
		c.BearerToken = k8sauthconfig.Secret(bearerToken)
	}
}

// WithRateLimits tunes the client-side QPS / Burst.
func WithRateLimits(qps float32, burst int) Option {
	return func(c *Config) { c.QPS = qps; c.Burst = burst }
}

// Error sentinels, classified by behaviour (errors.Is), per Law 5.
var (
	// ErrRestConfig wraps any failure resolving the rest.Config.
	ErrRestConfig = errors.New("kubeclient: resolve rest.Config")
	// ErrClientset wraps a typed-clientset construction failure.
	ErrClientset = errors.New("kubeclient: build clientset")
	// ErrDynamic wraps a dynamic-client construction failure.
	ErrDynamic = errors.New("kubeclient: build dynamic client")
	// ErrUnknownSource is returned for an unrecognised Config.Source string.
	ErrUnknownSource = errors.New("kubeclient: unknown source")
)

// New builds a Client from options — the §3.5 canonical New(required…, opts…)
// shape. With no options it uses SourceAuto.
func New(opts ...Option) (*Client, error) {
	var cfg Config
	for _, o := range opts {
		o(&cfg)
	}
	return FromConfig(cfg)
}

// FromConfig builds a Client from an already-loaded Config — the §3.5 canonical
// consume-config shape. It MUST NOT itself load config; the shikumi loader lives
// once, at main.
func FromConfig(cfg Config) (*Client, error) {
	src, err := parseSource(cfg.Source)
	if err != nil {
		return nil, err
	}
	restCfg, err := restConfig(src, cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRestConfig, err)
	}
	if cfg.QPS > 0 {
		restCfg.QPS = cfg.QPS
	}
	if cfg.Burst > 0 {
		restCfg.Burst = cfg.Burst
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrClientset, err)
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDynamic, err)
	}
	return &Client{Config: restCfg, Clientset: cs, Dynamic: dyn}, nil
}

// FromClients wraps already-constructed clients — the seam fake-client tests
// (and any caller that owns its own clients) use to build a Client without
// touching a real API server.
func FromClients(cs kubernetes.Interface, dyn dynamic.Interface) *Client {
	return &Client{Clientset: cs, Dynamic: dyn}
}

func parseSource(s string) (Source, error) {
	switch s {
	case "", "auto":
		return SourceAuto, nil
	case "in-cluster":
		return SourceInCluster, nil
	case "kubeconfig":
		return SourceKubeconfig, nil
	case "auth-config":
		return SourceAuthConfig, nil
	default:
		return SourceAuto, fmt.Errorf("%w: %q", ErrUnknownSource, s)
	}
}

// restConfig resolves the *rest.Config for the chosen source. The kubeconfig
// path delegates to clientcmd, which natively honours `exec:` credential
// plugins — that is the GKE / EKS no-temp-SA path: the plugin
// (gke-gcloud-auth-plugin / `aws eks get-token`) mints a short-lived token on
// each request, no static service account required.
func restConfig(src Source, cfg Config) (*rest.Config, error) {
	switch src {
	case SourceInCluster:
		return rest.InClusterConfig()
	case SourceAuthConfig:
		rc := &rest.Config{
			Host:        cfg.Auth.Host,
			BearerToken: cfg.BearerToken.Reveal(),
		}
		rc.TLSClientConfig = rest.TLSClientConfig{
			Insecure: cfg.Auth.InsecureSkipTLSVerify,
			CAData:   []byte(cfg.Auth.CACert),
		}
		if cfg.Auth.InsecureSkipTLSVerify {
			rc.TLSClientConfig.CAData = nil
		}
		return rc, nil
	case SourceKubeconfig:
		return loadKubeconfig(cfg.KubeconfigPath, cfg.Context)
	case SourceAuto:
		if rc, err := rest.InClusterConfig(); err == nil {
			return rc, nil
		}
		return loadKubeconfig("", cfg.Context)
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnknownSource, src)
	}
}

// loadKubeconfig builds a rest.Config from a kubeconfig file (or the default
// loading rules when path is empty), honouring exec-credential plugins.
func loadKubeconfig(path, contextName string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.ExplicitPath = path
	}
	overrides := &clientcmd.ConfigOverrides{}
	if contextName != "" {
		overrides.CurrentContext = contextName
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}
