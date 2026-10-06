// Command exnode builds and serves Externalize proof bundles from Stellar
// history archives and RPC.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Externalize-Labs/externalize-node/internal/archive"
	"github.com/Externalize-Labs/externalize-node/internal/bundle"
	"github.com/Externalize-Labs/externalize-node/internal/rpc"
	"github.com/Externalize-Labs/externalize-node/internal/server"
)

var version = "dev"

var networks = map[string]struct{ passphrase, archive string }{
	"public":  {"Public Global Stellar Network ; September 2015", sdfMirrors("core-live/core_live")},
	"testnet": {"Test SDF Network ; September 2015", sdfMirrors("core-testnet/core_testnet")},
}

// sdfMirrors lists SDF's three history archives for a network.
func sdfMirrors(prefix string) string {
	var urls []string
	for i := 1; i <= 3; i++ {
		urls = append(urls, fmt.Sprintf("https://history.stellar.org/prd/%s_%03d", prefix, i))
	}
	return strings.Join(urls, ",")
}

const usage = `exnode builds Externalize proof bundles from history archives and Stellar RPC.

Usage:
  exnode bundle [flags]   write one bundle to stdout
  exnode serve  [flags]   serve bundles over HTTP
  exnode version

Run "exnode <command> -h" for flags. Every flag can also be set with an
EXNODE_* environment variable (e.g. EXNODE_RPC).
`

type common struct {
	network, archive, rpc, cache string
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.network, "network", env("EXNODE_NETWORK", "public"), "public or testnet")
	fs.StringVar(&c.archive, "archive", env("EXNODE_ARCHIVE", ""), "history archive mirror URLs, comma-separated (default: SDF's three archives for the network)")
	fs.StringVar(&c.rpc, "rpc", env("EXNODE_RPC", ""), "Stellar RPC URL (needed for invocations and transaction lookups)")
	fs.StringVar(&c.cache, "cache", env("EXNODE_CACHE", ""), "directory for cached checkpoint files")
}

func (c *common) builder() (*bundle.Builder, error) {
	n, ok := networks[c.network]
	if !ok {
		return nil, fmt.Errorf("unknown network %q (want public or testnet)", c.network)
	}
	arch := c.archive
	if arch == "" {
		arch = n.archive
	}
	b := &bundle.Builder{Network: n.passphrase, Archive: archive.NewClient(arch, c.cache)}
	if c.rpc != "" {
		b.RPC = rpc.NewClient(c.rpc)
	}
	return b, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	archive.UserAgent = "exnode/" + version
	rpc.UserAgent = "exnode/" + version
	var err error
	switch os.Args[1] {
	case "bundle":
		err = runBundle(os.Args[2:])
	case "serve":
		err = runServe(os.Args[2:])
	case "version":
		fmt.Println("exnode", version)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func runBundle(args []string) error {
	fs := flag.NewFlagSet("bundle", flag.ExitOnError)
	var c common
	c.register(fs)
	ledger := fs.Uint("ledger", 0, "ledger sequence (optional when a transaction is given)")
	var txs, invs list
	fs.Var(&txs, "tx", "prove a transaction was applied (repeatable)")
	fs.Var(&invs, "invocation", "prove a contract call's return value and events: <hash>[:<op>] (repeatable)")
	txset := fs.Bool("txset", false, "include the full transaction set")
	_ = fs.Parse(args)

	b, err := c.builder()
	if err != nil {
		return err
	}
	req := bundle.Request{Ledger: uint32(*ledger), Transactions: txs, WithTxSet: *txset}
	for _, v := range invs {
		hash, op, _ := strings.Cut(v, ":")
		n, err := strconv.ParseUint(defaultTo(op, "0"), 10, 32)
		if err != nil {
			return fmt.Errorf("invalid invocation %q", v)
		}
		req.Invocations = append(req.Invocations, bundle.Invocation{TxHash: hash, OpIndex: uint32(n)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := b.Build(ctx, req)
	if err != nil {
		return err
	}
	return bundle.Encode(os.Stdout, out)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var c common
	c.register(fs)
	addr := fs.String("addr", env("EXNODE_ADDR", ":8080"), "listen address")
	_ = fs.Parse(args)

	b, err := c.builder()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	srv := &http.Server{
		Addr:              *addr,
		Handler:           (&server.Server{Builder: b, Log: log}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      5 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("serving", "addr", *addr, "network", c.network, "rpc", c.rpc != "")
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func defaultTo(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
