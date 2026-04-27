package server

import (
	"context"
	"testing"
	"time"

	"github.com/Catker/acmeDeliver/pkg/config"
)

func TestServerRun_RespectsContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()

	srv, err := NewServer(&config.Config{
		Bind:    "127.0.0.1",
		Port:    "0",
		BaseDir: tmpDir,
		Key:     "test-key",
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- srv.Run(ctx)
	}()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run(ctx) error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run(ctx) 未在上下文取消后及时退出")
	}
}
