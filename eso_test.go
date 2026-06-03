package kubeclient

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// newES builds an unstructured ExternalSecret with the given data/dataFrom keys.
func newES(name, namespace string, dataKeys, extractKeys []string) *unstructured.Unstructured {
	var data []any
	for _, k := range dataKeys {
		data = append(data, map[string]any{
			"secretKey": k,
			"remoteRef": map[string]any{"key": k},
		})
	}
	var dataFrom []any
	for _, k := range extractKeys {
		dataFrom = append(dataFrom, map[string]any{
			"extract": map[string]any{"key": k},
		})
	}
	es := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "external-secrets.io/v1beta1",
		"kind":       "ExternalSecret",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]any{},
	}}
	if data != nil {
		_ = unstructured.SetNestedSlice(es.Object, data, "spec", "data")
	}
	if dataFrom != nil {
		_ = unstructured.SetNestedSlice(es.Object, dataFrom, "spec", "dataFrom")
	}
	return es
}

func TestRemoteKeysOf(t *testing.T) {
	tests := []struct {
		name string
		es   *unstructured.Unstructured
		want []string
	}{
		{
			name: "nil is empty",
			es:   nil,
			want: nil,
		},
		{
			name: "data remoteRef keys",
			es:   newES("a", "ns", []string{"db/password", "db/username"}, nil),
			want: []string{"db/password", "db/username"},
		},
		{
			name: "dataFrom extract keys",
			es:   newES("b", "ns", nil, []string{"app/config"}),
			want: []string{"app/config"},
		},
		{
			name: "combined, deduplicated",
			es:   newES("c", "ns", []string{"shared/key"}, []string{"shared/key", "other/key"}),
			want: []string{"shared/key", "other/key"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RemoteKeysOf(tt.es)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// fakeESOScheme registers the ExternalSecret list kind so the fake dynamic
// client + informer can List the CRD.
func fakeESOScheme() (*runtime.Scheme, map[schema.GroupVersionResource]string) {
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		ExternalSecretGVR: "ExternalSecretList",
	}
	return scheme, gvrToListKind
}

func TestESOIndexerByRemoteKey(t *testing.T) {
	scheme, listKinds := fakeESOScheme()
	objs := []runtime.Object{
		newES("alpha", "team-a", []string{"prod/db/password"}, nil),
		newES("beta", "team-a", []string{"prod/db/password", "prod/api/token"}, nil),
		newES("gamma", "team-b", nil, []string{"prod/config/bundle"}),
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objs...)
	client := FromClients(nil, dyn)

	idx, err := client.NewESOIndexer()
	if err != nil {
		t.Fatalf("NewESOIndexer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := idx.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !idx.HasSynced() {
		t.Fatal("HasSynced = false after Start")
	}

	// prod/db/password is referenced by alpha + beta.
	matches, err := idx.ByRemoteKey("prod/db/password")
	if err != nil {
		t.Fatalf("ByRemoteKey: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("ByRemoteKey(prod/db/password) = %d ExternalSecrets, want 2", len(matches))
	}

	// prod/config/bundle (dataFrom.extract.key) is referenced by gamma only.
	matches, err = idx.ByRemoteKey("prod/config/bundle")
	if err != nil {
		t.Fatalf("ByRemoteKey: %v", err)
	}
	if len(matches) != 1 || matches[0].GetName() != "gamma" {
		t.Fatalf("ByRemoteKey(prod/config/bundle) = %v, want [gamma]", matches)
	}

	// an unreferenced key yields nothing.
	matches, err = idx.ByRemoteKey("does/not/exist")
	if err != nil {
		t.Fatalf("ByRemoteKey: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("ByRemoteKey(does/not/exist) = %d, want 0", len(matches))
	}

	keys := idx.RemoteKeys()
	sort.Strings(keys)
	want := []string{"prod/api/token", "prod/config/bundle", "prod/db/password"}
	if len(keys) != len(want) {
		t.Fatalf("RemoteKeys = %v, want %v", keys, want)
	}
	for i := range keys {
		if keys[i] != want[i] {
			t.Fatalf("RemoteKeys = %v, want %v", keys, want)
		}
	}
}

func TestESOIndexerCustomKeyFunc(t *testing.T) {
	scheme, listKinds := fakeESOScheme()
	objs := []runtime.Object{newES("alpha", "ns", []string{"ignored"}, nil)}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objs...)
	client := FromClients(nil, dyn)

	// A custom extractor that keys by name instead of remoteRef — proves the
	// generic seam.
	idx, err := client.NewESOIndexer(WithRemoteKeyFunc(func(es *unstructured.Unstructured) []string {
		return []string{"name:" + es.GetName()}
	}))
	if err != nil {
		t.Fatalf("NewESOIndexer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := idx.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	matches, err := idx.ByRemoteKey("name:alpha")
	if err != nil {
		t.Fatalf("ByRemoteKey: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("custom keyfunc index miss: got %d", len(matches))
	}
}

func TestESOIndexerNoDynamicClient(t *testing.T) {
	client := FromClients(nil, nil)
	_, err := client.NewESOIndexer()
	if !errors.Is(err, ErrNoDynamicClient) {
		t.Fatalf("err = %v, want ErrNoDynamicClient", err)
	}
}

func TestListExternalSecrets(t *testing.T) {
	scheme, listKinds := fakeESOScheme()
	objs := []runtime.Object{
		newES("alpha", "team-a", []string{"k1"}, nil),
		newES("beta", "team-a", []string{"k2"}, nil),
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objs...)
	client := FromClients(nil, dyn)

	list, err := client.ListExternalSecrets(context.Background(), "team-a")
	if err != nil {
		t.Fatalf("ListExternalSecrets: %v", err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("listed %d items, want 2", len(list.Items))
	}
}

var _ = metav1.ListOptions{} // metav1 used via ListExternalSecrets
