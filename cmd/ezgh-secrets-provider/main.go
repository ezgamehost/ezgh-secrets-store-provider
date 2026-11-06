// Command ezgh-secrets-provider is the secrets-store add-on's provider "ezgh" for the Secrets Store
// CSI Driver: a host-network DaemonSet serving the driver's provider gRPC API on
// /var/run/secrets-store-csi-providers/ezgh.sock (secretsprovider.Provider).
package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"github.com/ezgamehost/ezgh-secrets-store-provider/internal/cloud/secretsprovider"
	"github.com/ezgamehost/ezgh-secrets-store-provider/internal/cloud/secretsprovider/v1alpha1"
	"github.com/ezgamehost/ezgh-secrets-store-provider/internal/kit/logging"
)

func main() {
	org := flag.String("org", "", "the cluster's organization")
	project := flag.String("project", "", "the project secrets are read from unless a SecretProviderClass names another")
	socket := flag.String("socket", "/var/run/secrets-store-csi-providers/"+secretsprovider.Name+".sock", "the provider socket")
	creds := flag.String("credentials-url", "http://169.254.170.23/v1/credentials", "the node's pod identity agent")
	flag.Parse()
	log := logging.New(os.Stdout, "ezgh-secrets-provider", os.Getenv("APP_ENV") == "local")
	if *org == "" || *project == "" {
		log.Error("-org and -project are required")
		os.Exit(2)
	}
	p := &secretsprovider.Provider{Org: *org, Project: *project, CredentialsURL: *creds, HTTP: &http.Client{
		// The agent is on the node: never through a proxy, which would see the pod's token.
		Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil},
	}}
	if err := serve(*socket, p, func(method, code, msg string) {
		log.Warn("mount failed", "method", method, "code", code, "error", msg)
	}); err != nil {
		log.Error("serving failed", "error", err.Error())
		os.Exit(1)
	}
}

// serve answers on the unix socket until SIGTERM or SIGINT, then lets running mounts finish.
// Failed calls are reported with their method and gRPC code, never their request (which holds
// the pod's token).
func serve(path string, p v1alpha1.CSIDriverProviderServer, failed func(method, code, msg string)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	srv := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		resp, err := h(ctx, req)
		if err != nil {
			s := status.Convert(err)
			failed(info.FullMethod, s.Code().String(), s.Message())
		}
		return resp, err
	}))
	v1alpha1.RegisterCSIDriverProviderServer(srv, p)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()
	return srv.Serve(lis)
}
