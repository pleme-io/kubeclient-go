# kubeclient-go

The fleet's one typed `client-go` helper — so no tool re-derives kubeconfig
loading, exec-credential wiring (GKE / EKS, **no temporary service account**),
pod exec / log streaming / file copy, or an ExternalSecret informer keyed by
remote key.

## What

A single import-gated leaf (it carries `k8s.io/client-go`, so a tool that does
not touch a Kubernetes API never pays for it) exposing:

- `Client` — the typed bundle of a resolved `*rest.Config` plus the typed and
  dynamic clients. Built via the canonical `New(opts…)` / `FromConfig(cfg)`
  shapes, or `FromClients(cs, dyn)` (the test / bring-your-own-clients seam).
- **kubeconfig + exec-credential**: `SourceKubeconfig` loads a kubeconfig and
  natively honours any `exec:` credential plugin (`gke-gcloud-auth-plugin`,
  `aws eks get-token`) — the GKE / EKS no-temp-SA path. `SourceInCluster`,
  `SourceAuto`, and `SourceAuthConfig` (an explicit `k8sauthconfig.Config` +
  bearer token) round out the origins.
- **pod helpers**: `Exec` (typed `kubectl exec`), `StreamLogs` / `ReadLogs`
  (`kubectl logs`), and `CopyToPod` (`kubectl cp`, via a streamed `tar`).
- **ESO informer/index**: `NewESOIndexer` wraps a SharedIndexInformer over the
  External-Secrets-Operator `ExternalSecret` CRD with a secondary index keyed by
  remote key, so `ByRemoteKey("prod/db/password")` answers "which ExternalSecrets
  reference this remote key?" in O(1) from the local cache. The key extractor is
  a pluggable `RemoteKeyFunc` (default `RemoteKeysOf` reads
  `spec.data[].remoteRef.key` + `spec.dataFrom[].extract.key`).

WORLDS-SEPARATE: this is a PUBLIC, provider-neutral primitive. It never names or
imports any specific secret backend — the ESO CRD's `remoteRef.key` is read
generically regardless of which SecretStore resolves it.

## Why

Migrators, validators, and diagnostics tools all re-derive the same client-go
plumbing: load a kubeconfig with exec-credential support, exec into a pod, tail
logs, copy a file, or walk ExternalSecrets to find what references a given
remote key. One typed helper means uniform credential resolution, uniform pod
plumbing, and one home for the remote-key index — never a hand-rolled
`clientcmd` + `remotecommand` + tar dance again.

## Install

```
go get github.com/pleme-io/kubeclient-go
```

## Usage

```go
import (
    kubeclient "github.com/pleme-io/kubeclient-go"
    k8sauthconfig "github.com/pleme-io/k8sauthconfig-go"
)

// kubeconfig + exec-credential (GKE/EKS, no temp SA)
c, err := kubeclient.New(kubeclient.WithKubeconfig("/home/me/.kube/config"),
    kubeclient.WithContext("gke_prod"))

// or an explicit auth tuple (shared k8sauthconfig domain type)
c, _ = kubeclient.New(kubeclient.WithAuthConfig(
    k8sauthconfig.Config{Host: "https://api:6443", CACert: caPEM}, bearerToken))

// pod exec / logs / copy
res, _ := c.Exec(ctx, kubeclient.PodRef{Namespace: "ns", Pod: "p"},
    kubeclient.ExecOptions{Command: []string{"sh", "-c", "id"}})
logs, _ := c.ReadLogs(ctx, kubeclient.PodRef{Namespace: "ns", Pod: "p"},
    kubeclient.LogOptions{TailLines: ptr(int64(100))})
_ = c.CopyToPod(ctx, kubeclient.PodRef{Namespace: "ns", Pod: "p"},
    "/etc/app/config.yaml", content)

// ExternalSecret informer keyed by remote key
idx, _ := c.NewESOIndexer()
_ = idx.Start(ctx)                              // blocks until cache sync
hits, _ := idx.ByRemoteKey("prod/db/password")  // O(1) local lookup
```

## Configuration

`Config` is the shikumi-loadable surface (yaml tags: `source`,
`kubeconfigPath`, `context`, `auth`, `bearerToken`, `qps`, `burst`). This
library never loads config — consumers `shikumi.For[Root](name)…Load(ctx)` once
at `main` and hand the sub-struct to `FromConfig`, per the GSDS §3.5 canonical
shape. The `bearerToken` field is a redacting `k8sauthconfig.Secret`.

## Release

Pull-model (Go modules): an annotated `vX.Y.Z` tag is the release; pkg.go.dev
indexes it. See the GSDS module delivery FSM.
