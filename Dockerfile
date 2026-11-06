# Build context: the repository root. The secrets-store add-on's provider image: one static binary
# on distroless, run as root (the driver's provider directory on the host is root's).
FROM mirror.gcr.io/library/golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ezgh-secrets-provider ./cmd/ezgh-secrets-provider

FROM gcr.io/distroless/static-debian12 AS runtime
COPY --from=build /out/ezgh-secrets-provider /ezgh-secrets-provider
ENTRYPOINT ["/ezgh-secrets-provider"]
