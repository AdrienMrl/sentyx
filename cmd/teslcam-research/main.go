// Command teslcam-research serves the local AI research dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/AdrienMrl/teslcam/internal/research"
)

func main() {
	fs := flag.NewFlagSet("teslcam-research", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8770", "address to listen on")
	dir := fs.String("dir", "bench/research", "research data directory")
	_ = fs.Parse(os.Args[1:])

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fatal(err)
	}
	srv := &http.Server{Handler: research.NewServer(*dir)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	fmt.Printf("research dashboard: http://%s\n", ln.Addr())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "teslcam-research:", err)
	os.Exit(1)
}
