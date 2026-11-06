# ezgh-secrets-store-provider

The [Secrets Store CSI Driver](https://secrets-store-csi-driver.sigs.k8s.io/) provider `ezgh` for EZGH
Kubernetes, the counterpart of secrets-store-csi-driver-provider-aws: it mounts EZGH Secrets fields
into pods as files, read with the pod's own pod identity key (from the node's pod identity agent at
`http://169.254.170.23/v1/credentials`), so a pod sees only the secrets its Bot may read. Rotation
follows the driver's rotation poll.

One binary, `ezgh-secrets-provider`, in the `ezgh-secrets-provider` image, run by the secrets-store
add-on's DaemonSet on every node. Flags: `-org`, `-project` (the default project a
SecretProviderClass reads from), `-socket` (default
`/var/run/secrets-store-csi-providers/ezgh.sock`), `-credentials-url`. Logs are JSON lines through the
vendored kit (`internal/kit/logging`).

## Build and test

Go 1.27.1 (`go.mod`).

| Check | Command |
| --- | --- |
| Build | `CGO_ENABLED=0 GOOS=linux go build ./cmd/ezgh-secrets-provider` |
| Image | `docker build .` (one static binary on distroless) |
| Tests | `go test -race $(go list ./... \| grep -v /internal/kit/)` |
| Lint | `gofmt -l .`, `go vet ./...`, `go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...` |
| Logging | `scripts/check-logging.sh` |

CI (`.github/workflows/ci.yml`) runs them and builds the image, without pushing it, on every pull
request and push to main.

## Releases

Push a tag `vX.Y.Z` on main. `.github/workflows/release.yml` publishes the GitHub release
(`ezgh-secrets-provider-linux-{amd64,arm64}`, `SHA256SUMS`) and pushes the linux/amd64 image to the
kubernetes-addons Registry as `<host>/kubernetes/ezgh-secrets-provider:<tag>`. That needs repository
secret `KUBERNETES_ADDONS_REGISTRY_KEY` (a platform-organization Bot key that can push to project
kubernetes-addons) and variable `KUBERNETES_ADDONS_REGISTRY_HOST` (`<hostId>.registry.ezghcloud.com`).
A published tag never moves: a fix ships as a new version.

kubernetes-stack's `secrets-store` add-on bundle pins the image in its `images.json`
(`ezgh-secrets-provider`).

## Copied code

Other repositories' code is copied in, not imported, each copy with its own sync:

| Path | Source | Sync |
| --- | --- | --- |
| `internal/kit` | ezgamehost/service-template `kit/go` (commit in `internal/kit/KIT_VERSION`) | `scripts/sync-kit.sh` |
| `internal/ezghclient` | ezgamehost/ezgh-cloud-controller (commit in `COPIES`); it is itself ezgh-cli's API clients plus that repository's additions | `scripts/sync-copies.sh ezgh-cloud-controller` |
| `internal/cloud/secretsprovider/v1alpha1/service.proto` | kubernetes-sigs/secrets-store-csi-driver v1.6.1, Apache-2.0, unedited (regenerate as `provider.go` says) | by hand |

## License

Apache-2.0 ([LICENSE](LICENSE)). Security reports: [SECURITY.md](SECURITY.md).
