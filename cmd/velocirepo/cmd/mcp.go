package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"
	mcpinternal "github.com/posit-dev/velocirepo/internal/mcp"
	"github.com/spf13/cobra"
)

func mcpCmd() *cobra.Command {
	var (
		httpAddr string
		readOnly bool
	)

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Start the MCP (Model Context Protocol) server",
		Long: `Start an MCP server that exposes velocirepo's functionality to AI assistants.

By default, the server uses stdio transport (for Claude Desktop / Claude Code).
Use --http to start a Streamable HTTP server instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := mcpinternal.NewServer(mcpinternal.ServerOptions{
				Config:   cfg,
				ReadOnly: readOnly,
			})

			if httpAddr != "" {
				return serveHTTP(cmd.Context(), s, httpAddr)
			}
			return serveStdio(cmd.Context(), s)
		},
	}

	cmd.Flags().StringVar(&httpAddr, "http", "", "start Streamable HTTP server on this address (e.g., 127.0.0.1:8080)")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "disable fetch and write tools")

	return cmd
}

func serveStdio(ctx context.Context, s *server.MCPServer) error {
	stdio := server.NewStdioServer(s)
	return stdio.Listen(ctx, os.Stdin, os.Stdout)
}

func serveHTTP(ctx context.Context, s *server.MCPServer, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", addr, err)
	}

	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		fmt.Fprintf(os.Stderr, "Warning: binding to non-loopback address %s\n", addr)
	}

	httpServer := server.NewStreamableHTTPServer(s)

	fmt.Fprintf(os.Stderr, "MCP server listening on http://%s\n", addr)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpServer.Start(addr)
	}()

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
	case <-sigCh:
		fmt.Fprintf(os.Stderr, "\nShutting down...\n")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}

	return nil
}
