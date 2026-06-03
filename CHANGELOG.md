# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-06-03

### Added
- Initial: typed `client-go` helper. `Client` via the canonical
  `New(opts…)` / `FromConfig(cfg)` / `FromClients(cs, dyn)` shapes.
- Kubeconfig + exec-credential loading (`SourceKubeconfig` honours `exec:`
  plugins — GKE `gke-gcloud-auth-plugin` / EKS `aws eks get-token`, no temp SA),
  plus `SourceInCluster`, `SourceAuto`, and `SourceAuthConfig` (explicit
  `k8sauthconfig.Config` + bearer token).
- Pod helpers: `Exec`, `StreamLogs` / `ReadLogs`, `CopyToPod` (streamed tar).
- `ESOIndexer` — a generic SharedIndexInformer wrapper over the
  External-Secrets-Operator `ExternalSecret` CRD, secondary-indexed by remote
  key (`ByRemoteKey` / `RemoteKeys`), with a pluggable `RemoteKeyFunc`
  (default `RemoteKeysOf`).
- Behaviour-classified error sentinels (`ErrRestConfig`/`ErrClientset`/
  `ErrDynamic`/`ErrUnknownSource`/`ErrNoExecutor`/`ErrExecFailed`/
  `ErrEmptyCommand`/`ErrNoDynamicClient`/`ErrSyncTimeout`).
