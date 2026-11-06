package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/ezgamehost/ezgh-secrets-store-provider/internal/cloud/secretsprovider"
	"github.com/ezgamehost/ezgh-secrets-store-provider/internal/cloud/secretsprovider/v1alpha1"
)

// The driver reaches the provider over its socket.
func TestServesOnSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "sp") // short: unix socket paths are limited to ~100 bytes
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "ezgh.sock")
	go func() { _ = serve(path, &secretsprovider.Provider{}, func(string, string, string) {}) }()
	conn, err := grpc.NewClient("unix://"+path, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, err := v1alpha1.NewCSIDriverProviderClient(conn).Version(ctx, &v1alpha1.VersionRequest{Version: "v1alpha1"}, grpc.WaitForReady(true))
	if err != nil || v.RuntimeName != "ezgh-secrets-provider" {
		t.Fatal(v, err)
	}
}
