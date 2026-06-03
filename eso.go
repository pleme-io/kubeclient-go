package kubeclient

import (
	"context"
	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

// ExternalSecretGVR is the External-Secrets-Operator ExternalSecret resource.
// The generic ESO CRD — never an akeyless-specific type. The backing
// SecretStore may be any provider; this primitive only reads the remoteRef keys.
var ExternalSecretGVR = schema.GroupVersionResource{
	Group:    "external-secrets.io",
	Version:  "v1beta1",
	Resource: "externalsecrets",
}

// remoteKeyIndex is the indexer name under which ExternalSecrets are keyed by
// their referenced remote keys.
const remoteKeyIndex = "remoteKey"

// ESOIndexer error sentinels, classified by behaviour (errors.Is).
var (
	// ErrNoDynamicClient is returned when the Client has no dynamic client.
	ErrNoDynamicClient = errors.New("kubeclient: no dynamic client for ESO informer")
	// ErrSyncTimeout is returned when the informer cache fails to sync.
	ErrSyncTimeout = errors.New("kubeclient: ESO informer cache failed to sync")
)

// RemoteKeyFunc extracts the set of remote keys an ExternalSecret references.
// The default (RemoteKeysOf) reads spec.data[].remoteRef.key and
// spec.dataFrom[].extract.key, but a consumer can supply any extractor — this
// is the generic seam (Law 5: behaviour, not a baked-in shape).
type RemoteKeyFunc func(es *unstructured.Unstructured) []string

// ESOIndexer is a generic informer wrapper over the ExternalSecret CRD with a
// secondary index keyed by remote key, so a consumer can answer "which
// ExternalSecrets reference remote key X?" in O(1) from the local cache without
// re-listing the API server.
type ESOIndexer struct {
	informer cache.SharedIndexInformer
	factory  dynamicinformer.DynamicSharedInformerFactory
	keyFunc  RemoteKeyFunc
}

// ESOOption configures an ESOIndexer.
type ESOOption func(*esoSettings)

type esoSettings struct {
	namespace string // "" = all namespaces
	keyFunc   RemoteKeyFunc
}

// WithNamespace scopes the informer to a single namespace ("" = all).
func WithNamespace(ns string) ESOOption { return func(s *esoSettings) { s.namespace = ns } }

// WithRemoteKeyFunc overrides the remote-key extractor.
func WithRemoteKeyFunc(f RemoteKeyFunc) ESOOption {
	return func(s *esoSettings) { s.keyFunc = f }
}

// NewESOIndexer builds an ESOIndexer over the Client's dynamic client — the
// §3.5 canonical New(required…, opts…) shape (the Client is the required arg).
func (c *Client) NewESOIndexer(opts ...ESOOption) (*ESOIndexer, error) {
	if c.Dynamic == nil {
		return nil, ErrNoDynamicClient
	}
	settings := esoSettings{keyFunc: RemoteKeysOf}
	for _, o := range opts {
		o(&settings)
	}
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		c.Dynamic, 0, settings.namespace, nil,
	)
	informer := factory.ForResource(ExternalSecretGVR).Informer()
	idx := &ESOIndexer{informer: informer, factory: factory, keyFunc: settings.keyFunc}
	if err := informer.AddIndexers(cache.Indexers{
		remoteKeyIndex: func(obj any) ([]string, error) {
			es, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return nil, nil
			}
			return idx.keyFunc(es), nil
		},
	}); err != nil {
		return nil, err
	}
	return idx, nil
}

// Start runs the informer until ctx is cancelled and blocks until the initial
// cache sync completes (or ctx fires).
func (e *ESOIndexer) Start(ctx context.Context) error {
	e.factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), e.informer.HasSynced) {
		return ErrSyncTimeout
	}
	return nil
}

// HasSynced reports whether the initial cache sync has completed.
func (e *ESOIndexer) HasSynced() bool { return e.informer.HasSynced() }

// ByRemoteKey returns every cached ExternalSecret that references the given
// remote key — the O(1) local lookup this primitive exists to provide.
func (e *ESOIndexer) ByRemoteKey(remoteKey string) ([]*unstructured.Unstructured, error) {
	objs, err := e.informer.GetIndexer().ByIndex(remoteKeyIndex, remoteKey)
	if err != nil {
		return nil, err
	}
	out := make([]*unstructured.Unstructured, 0, len(objs))
	for _, o := range objs {
		if es, ok := o.(*unstructured.Unstructured); ok {
			out = append(out, es)
		}
	}
	return out, nil
}

// RemoteKeys lists every distinct remote key currently in the index.
func (e *ESOIndexer) RemoteKeys() []string {
	return e.informer.GetIndexer().ListIndexFuncValues(remoteKeyIndex)
}

// AddEventHandler registers a resource-event handler on the informer (add /
// update / delete) so consumers can react to ExternalSecret changes.
func (e *ESOIndexer) AddEventHandler(h cache.ResourceEventHandler) error {
	_, err := e.informer.AddEventHandler(h)
	return err
}

// RemoteKeysOf is the default RemoteKeyFunc: it reads spec.data[].remoteRef.key
// and spec.dataFrom[].extract.key from an ExternalSecret. It is provider-neutral
// — the same shape regardless of which SecretStore backend the key resolves
// against.
func RemoteKeysOf(es *unstructured.Unstructured) []string {
	if es == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var keys []string
	add := func(k string) {
		if k == "" {
			return
		}
		if _, ok := seen[k]; ok {
			return
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
	}

	data, _, _ := unstructured.NestedSlice(es.Object, "spec", "data")
	for _, d := range data {
		m, ok := d.(map[string]any)
		if !ok {
			continue
		}
		if ref, ok := m["remoteRef"].(map[string]any); ok {
			if k, ok := ref["key"].(string); ok {
				add(k)
			}
		}
	}

	dataFrom, _, _ := unstructured.NestedSlice(es.Object, "spec", "dataFrom")
	for _, d := range dataFrom {
		m, ok := d.(map[string]any)
		if !ok {
			continue
		}
		if extract, ok := m["extract"].(map[string]any); ok {
			if k, ok := extract["key"].(string); ok {
				add(k)
			}
		}
		if find, ok := m["find"].(map[string]any); ok {
			if name, ok := find["name"].(map[string]any); ok {
				if rx, ok := name["regexp"].(string); ok {
					add(rx)
				}
			}
		}
	}
	return keys
}

// ListExternalSecrets is a one-shot dynamic list — the non-informer path for a
// CLI that just needs the current set once (no watch lifecycle).
func (c *Client) ListExternalSecrets(ctx context.Context, namespace string) (*unstructured.UnstructuredList, error) {
	if c.Dynamic == nil {
		return nil, ErrNoDynamicClient
	}
	return c.Dynamic.Resource(ExternalSecretGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
}
