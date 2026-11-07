# ezgh-secrets-store-provider

The EZGH Secrets provider for the
[Secrets Store CSI Driver](https://secrets-store-csi-driver.sigs.k8s.io/) on
[EZGH Kubernetes](https://docs.ezghcloud.com). It mounts EZGH Secrets fields into pods as files.

EZGH Kubernetes installs and upgrades this provider as the `secrets-store` add-on. This repository
holds its source code; you don't need to deploy it yourself.

## How it works

- Each mount is read with the pod's own credentials from
  [ezgh-pod-identity](https://github.com/ezgamehost/ezgh-pod-identity), so a pod can only read the
  secrets its Bot is allowed to read.
- Secrets are selected with a `SecretProviderClass` using provider `ezgh`.
- Mounted files are refreshed on the driver's rotation interval.

## Configuration

One binary, `ezgh-secrets-provider`, runs on every node as a DaemonSet.

| Flag | Description |
| --- | --- |
| `-org` | The organization that owns the cluster |
| `-project` | The default project a `SecretProviderClass` reads from |
| `-socket` | The provider socket (default `/var/run/secrets-store-csi-providers/ezgh.sock`) |
| `-credentials-url` | The pod identity agent's credentials endpoint |

Logs are structured JSON.

## Container image and binaries

| Artifact | Location |
| --- | --- |
| Container image (linux/amd64) | `q5zljyjh52il.registry.ezghcloud.com/kubernetes/ezgh-secrets-provider:<version>` |
| Binaries (linux/amd64, linux/arm64) with `SHA256SUMS` | [GitHub Releases](https://github.com/ezgamehost/ezgh-secrets-store-provider/releases) |

## Building from source

Requires Go 1.27.1 or later.

```sh
CGO_ENABLED=0 GOOS=linux go build ./cmd/ezgh-secrets-provider
docker build .
```

## Testing

| Check | Command |
| --- | --- |
| Unit tests | `go test -race $(go list ./... \| grep -v /internal/kit/)` |
| Lint | `gofmt -l .`, `go vet` and `staticcheck` |
| Logging | `scripts/check-logging.sh` |

Continuous integration runs these and builds the image on every pull request and push to `main`.

## Releases

Releases follow semantic versioning. Pushing a `vX.Y.Z` tag on `main` publishes the container image
and then the binaries. Published versions are immutable; fixes ship as new versions.

## Vendored and third-party code

`internal/kit` and `internal/ezghclient` are vendored from shared EZGH libraries and refreshed with
`scripts/sync-kit.sh` and `scripts/sync-copies.sh`. Changes to them are made upstream.

`internal/cloud/secretsprovider/v1alpha1/service.proto` is the provider API from
[kubernetes-sigs/secrets-store-csi-driver](https://github.com/kubernetes-sigs/secrets-store-csi-driver)
v1.6.1, used unmodified under the Apache License 2.0.

## Security

Please report vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

## License

Licensed under the [Apache License 2.0](LICENSE).
