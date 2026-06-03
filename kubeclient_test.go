package kubeclient

import (
	"errors"
	"testing"

	k8sauthconfig "github.com/pleme-io/k8sauthconfig-go"
)

func TestParseSource(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Source
		wantErr error
	}{
		{name: "empty is auto", in: "", want: SourceAuto},
		{name: "auto", in: "auto", want: SourceAuto},
		{name: "in-cluster", in: "in-cluster", want: SourceInCluster},
		{name: "kubeconfig", in: "kubeconfig", want: SourceKubeconfig},
		{name: "auth-config", in: "auth-config", want: SourceAuthConfig},
		{name: "garbage rejected", in: "nope", wantErr: ErrUnknownSource},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSource(tt.in)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

// testCAPEM is a minimal valid self-signed CA, so the real client-go TLS
// validation in NewForConfig accepts CAData and we can assert it was threaded.
const testCAPEM = `-----BEGIN CERTIFICATE-----
MIIBhTCCASugAwIBAgIQIRi6zePL6mKjOipn+dNuaTAKBggqhkjOPQQDAjASMRAw
DgYDVQQKEwdBY21lIENvMB4XDTE3MTAyMDE5NDMwNloXDTE4MTAyMDE5NDMwNlow
EjEQMA4GA1UEChMHQWNtZSBDbzBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABD0d
7VNhbWvZLWPuj/RtHFjvtJBEwOkhbN/BnnE8rnZR8+sbwnc/KhCk3FhnpHZnQz7B
5aETbbIgmuvewdjvSBSjYzBhMA4GA1UdDwEB/wQEAwICpDATBgNVHSUEDDAKBggr
BgEFBQcDATAPBgNVHRMBAf8EBTADAQH/MCkGA1UdEQQiMCCCDmxvY2FsaG9zdDo1
NDUzgg4xMjcuMC4wLjE6NTQ1MzAKBggqhkjOPQQDAgNIADBFAiEA2zpJEPQyz6/l
Wf86aX6PepsntZv2GYlA5UpabfT2EZICICpJ5h/iI+i341gBmLiAFQOyTDT+/wQc
6MF9+Yw1Yy0t
-----END CERTIFICATE-----`

func TestFromConfigAuthConfig(t *testing.T) {
	// SourceAuthConfig builds a rest.Config without contacting any API server,
	// so this exercises the full FromConfig path offline.
	cfg := Config{
		Source:      "auth-config",
		Auth:        k8sauthconfig.Config{Host: "https://api.cluster.example:6443", CACert: testCAPEM},
		BearerToken: k8sauthconfig.Secret("t-secret-token"),
		QPS:         50,
		Burst:       100,
	}
	c, err := FromConfig(cfg)
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	if c.Config.Host != "https://api.cluster.example:6443" {
		t.Fatalf("host = %q", c.Config.Host)
	}
	if c.Config.BearerToken != "t-secret-token" {
		t.Fatalf("bearer token not threaded onto rest.Config")
	}
	if string(c.Config.TLSClientConfig.CAData) != testCAPEM {
		t.Fatalf("CAData = %q", c.Config.TLSClientConfig.CAData)
	}
	if c.Config.QPS != 50 || c.Config.Burst != 100 {
		t.Fatalf("rate limits not applied: qps=%v burst=%v", c.Config.QPS, c.Config.Burst)
	}
	if c.Clientset == nil || c.Dynamic == nil {
		t.Fatal("clientset / dynamic not constructed")
	}
}

func TestFromConfigAuthConfigInsecure(t *testing.T) {
	cfg := Config{
		Source: "auth-config",
		Auth:   k8sauthconfig.Config{Host: "https://api", InsecureSkipTLSVerify: true},
	}
	c, err := FromConfig(cfg)
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	if !c.Config.TLSClientConfig.Insecure {
		t.Fatal("insecure not threaded")
	}
	if c.Config.TLSClientConfig.CAData != nil {
		t.Fatal("CAData must be cleared when insecure")
	}
}

func TestFromConfigUnknownSource(t *testing.T) {
	_, err := FromConfig(Config{Source: "bogus"})
	if !errors.Is(err, ErrUnknownSource) {
		t.Fatalf("err = %v, want ErrUnknownSource", err)
	}
}

func TestNewDefaultsToAuto(t *testing.T) {
	// New() with no options resolves SourceAuto. In a test environment there is
	// no in-cluster config and (usually) no kubeconfig, so we only assert that
	// it does not panic and yields a typed error path, not a successful client.
	_, err := New(WithRateLimits(10, 20))
	if err == nil {
		// A developer machine may have a kubeconfig — that is also valid.
		return
	}
	if !errors.Is(err, ErrRestConfig) {
		t.Fatalf("err = %v, want ErrRestConfig", err)
	}
}
