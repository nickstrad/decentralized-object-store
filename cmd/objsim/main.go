package main

import (
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

//go:embed help.txt
var help string

func main() {
	os.Exit(run(os.Args[0], os.Args[1:], scenarios(), os.Stdout, os.Stderr))
}

func find(all []sim.Scenario, name string) (sim.Scenario, bool) {
	for _, sc := range all {
		if sc.Name == name {
			return sc, true
		}
	}
	return sim.Scenario{}, false
}

func run(prog string, args []string, all []sim.Scenario, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("objsim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, help) }
	name := fs.String("scenario", "", "")
	seed := fs.Int64("seed", 1, "")
	list := fs.Bool("list", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *list {
		for _, sc := range all {
			fmt.Fprintln(stdout, sc.Name)
		}
		return 0
	}
	sc, ok := find(all, *name)
	if !ok {
		fmt.Fprintf(stderr, "objsim: unknown scenario %q\n", *name)
		return 2
	}
	s := sc.Run(*seed)
	d := s.Digests()
	fmt.Fprintf(stdout, "scenario=%s seed=%d steps=%d time=%d\nevents=%x\nstate=%x\n", sc.Name, *seed, s.Steps(), s.Now(), d.Events, d.State)
	if err := s.Verify(); err != nil {
		fmt.Fprintf(stderr, "objsim: %v\n", err)
		report(stderr, s)
		fmt.Fprintf(stderr, "REPLAY: %s -scenario %s -seed %d\n", command(prog), quote(sc.Name), *seed)
		return 1
	}
	return 0
}

const tailLines = 20

func report(w io.Writer, s *sim.Sim) {
	tail := s.LogTail(tailLines)
	fmt.Fprintf(w, "event log tail (%d of %d):\n", len(tail), s.LogLen())
	for _, line := range tail {
		fmt.Fprintf(w, "  %s\n", line)
	}
	var pe *sim.PanicError
	if errors.As(s.Err(), &pe) && len(pe.Stack) > 0 {
		fmt.Fprintf(w, "panic stack:\n%s\n", strings.TrimRight(string(pe.Stack), "\n"))
	}
}

func command(prog string) string {
	if strings.Contains(filepath.ToSlash(prog), "/go-build") {
		return "go run ./cmd/objsim"
	}
	return quote(prog)
}

func quote(arg string) string {
	if arg != "" && strings.Trim(arg, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./=:,+@%-") == "" {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}
