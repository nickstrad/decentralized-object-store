// Command objctl is a command-line client for the object store's HTTP API.
// The command tree (cobra) lives in this package; the verbs' behaviour is in
// internal/objctl and the HTTP transport in internal/storage/httpclient. Run
// "objctl --help" for usage and the exit codes.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/nickstrad/object-store/internal/objctl"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
	"github.com/nickstrad/object-store/internal/storage/httpclient"
)

func newService(addr string, o objctl.ServiceOptions) (httpapi.Service, error) {
	return httpclient.New(addr, httpclient.Options{Trace: o.Trace})
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], config{getenv: os.Getenv, Env: objctl.Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		Open: osOpen, Create: osCreate, NewService: newService}})
	stop()
	os.Exit(code)
}
