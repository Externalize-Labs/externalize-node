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
	"path/filepath"
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
  exnode status [flags]   show the archive tip and how far it trails RPC
  exnode cache prune --cache DIR --max-size 1GB
                          delete the oldest cached checkpoint files
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

func (c *common) archiveURL() string {
	if c.archive != "" {
		return c.archive
	}
	return networks[c.network].archive
}

func (c *common) archiveClient() *archive.Client {
	return archive.NewClient(c.archiveURL(), c.cache)
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
	case "status":
		err = runStatus(os.Args[2:])
	case "cache":
		err = runCache(os.Args[2:])
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
	var txs, invs, contracts list
	fs.Var(&txs, "tx", "prove a transaction was applied (repeatable)")
	fs.Var(&invs, "invocation", "prove a contract call's return value and events: <hash>[:<op>] (repeatable)")
	fs.Var(&contracts, "contract", "prove every invocation in --ledger that emitted events from this contract (repeatable)")
	txset := fs.Bool("txset", false, "include the full transaction set")
	outPath := fs.String("out", "", "write the bundle to this file instead of stdout")
	_ = fs.Parse(args)

	b, err := c.builder()
	if err != nil {
		return err
	}
	req := bundle.Request{Ledger: uint32(*ledger), Transactions: txs, Contracts: contracts, WithTxSet: *txset}
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
	if *outPath == "" {
		return bundle.Encode(os.Stdout, out)
	}
	// Write to a temporary file first so a failed run never leaves half a bundle.
	tmp, err := os.CreateTemp(filepath.Dir(*outPath), ".bundle-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := bundle.Encode(tmp, out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), *outPath); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s: ledger %d, %d claim(s)\n", *outPath, req.Ledger, len(out.Claims))
	return nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var c common
	c.register(fs)
	addr := fs.String("addr", env("EXNODE_ADDR", ":8080"), "listen address")
	rate := fs.Float64("rate", 5, "requests per second per client IP on /v1 routes (0 disables)")
	burst := fs.Float64("burst", 20, "requests a client may burst before the rate applies")
	cors := fs.String("cors", env("EXNODE_CORS", ""), "browser origins allowed to call the API, comma-separated (\"*\" for any)")
	_ = fs.Parse(args)

	b, err := c.builder()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	s := &server.Server{Builder: b, Log: log, ArchiveTip: c.archiveClient().Tip, RatePerSecond: *rate, RateBurst: *burst}
	for _, o := range strings.Split(*cors, ",") {
		if o = strings.TrimSpace(o); o != "" {
			s.CORSOrigins = append(s.CORSOrigins, o)
		}
	}
	if c.rpc != "" {
		s.RPCLatest = rpc.NewClient(c.rpc).LatestLedger
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           s.Handler(),
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

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	var c common
	c.register(fs)
	_ = fs.Parse(args)
	if _, ok := networks[c.network]; !ok && c.archive == "" {
		return fmt.Errorf("unknown network %q (want public or testnet)", c.network)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tip, err := c.archiveClient().Tip(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("archive tip   %d (next checkpoint %d)\n", tip, tip+archive.CheckpointFrequency)
	if c.rpc == "" {
		return nil
	}
	latest, err := rpc.NewClient(c.rpc).LatestLedger(ctx)
	if err != nil {
		return err
	}
	lag := int64(latest) - int64(tip)
	fmt.Printf("rpc latest    %d\narchive lag   %d ledgers (~%ds)\n", latest, lag, lag*6)
	return nil
}

func runCache(args []string) error {
	if len(args) == 0 || args[0] != "prune" {
		return errors.New("usage: exnode cache prune --cache DIR --max-size SIZE")
	}
	fs := flag.NewFlagSet("cache prune", flag.ExitOnError)
	dir := fs.String("cache", env("EXNODE_CACHE", ""), "cache directory")
	size := fs.String("max-size", "1GB", "size to prune down to (e.g. 500MB, 2GB)")
	_ = fs.Parse(args[1:])
	if *dir == "" {
		return errors.New("--cache is required")
	}
	limit, err := parseSize(*size)
	if err != nil {
		return err
	}
	res, err := archive.Prune(*dir, limit)
	if err != nil {
		return err
	}
	fmt.Printf("removed %d of %d files; %d -> %d bytes\n", res.Removed, res.Files, res.Before, res.After)
	return nil
}

// parseSize reads sizes like 1500, 500MB or 2GB (powers of 1024).
func parseSize(s string) (int64, error) {
	units := []struct {
		suffix string
		mult   int64
	}{{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}}
	upper := strings.ToUpper(strings.TrimSpace(s))
	for _, u := range units {
		if num, ok := strings.CutSuffix(upper, u.suffix); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("invalid size %q", s)
			}
			return n * u.mult, nil
		}
	}
	n, err := strconv.ParseInt(upper, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n, nil
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
