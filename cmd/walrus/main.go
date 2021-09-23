package main

import (
	"log"
	"net/http"
	"path/filepath"
	"runtime"

	"go.sia.tech/siad/build"
	"go.sia.tech/siad/modules/consensus"
	"go.sia.tech/siad/modules/gateway"
	"go.sia.tech/siad/modules/transactionpool"
	"lukechampine.com/flagg"
	"lukechampine.com/us/wallet"
	"lukechampine.com/walrus"
)

var (
	// to be supplied at build time
	githash   = "?"
	builddate = "?"
)

var (
	rootUsage = `Usage:
    walrus [flags]

Initializes the wallet and begins serving the walrus API.
`
	versionUsage = rootUsage

	resetUsage = `Usage:
    walrus reset

Resets the wallet's knowledge of the blockchain. All transactions and UTXOs
will be forgotten, and the next time the wallet starts, it will begin scanning
from the genesis block. This takes a long time! Resetting is typically only
necessary if you want to track addresses that have already appeared on the
blockchain.
`
)

var usage = flagg.SimpleUsage(flagg.Root, rootUsage)

func main() {
	log.SetFlags(0)

	rootCmd := flagg.Root
	rootCmd.Usage = flagg.SimpleUsage(rootCmd, rootUsage)
	addr := rootCmd.String("http", ":9380", "host:port to serve on")
	dir := rootCmd.String("dir", ".", "directory to store in")
	versionCmd := flagg.New("version", versionUsage)
	resetCmd := flagg.New("reset", resetUsage)
	resetDir := resetCmd.String("dir", ".", "directory where wallet is stored")

	cmd := flagg.Parse(flagg.Tree{
		Cmd: rootCmd,
		Sub: []flagg.Tree{
			{Cmd: versionCmd},
			{Cmd: resetCmd},
		},
	})
	args := cmd.Args()

	switch cmd {
	case versionCmd:
		if len(args) > 0 {
			usage()
			return
		}
		log.Printf("walrus v0.3.0\nCommit:     %s\nRelease:    %s\nGo version: %s %s/%s\nBuild Date: %s\n",
			githash, build.Release, runtime.Version(), runtime.GOOS, runtime.GOARCH, builddate)

	case rootCmd:
		if len(args) != 0 {
			rootCmd.Usage()
			return
		}
		if err := start(*dir, *addr); err != nil {
			log.Fatal(err)
		}

	case resetCmd:
		if len(args) != 0 {
			resetCmd.Usage()
			return
		}
		if err := reset(*resetDir); err != nil {
			log.Fatal(err)
		}
	}
}

func start(dir string, APIaddr string) error {
	g, err := gateway.New(":9381", true, filepath.Join(dir, "gateway"))
	if err != nil {
		return err
	}
	cs, errChan := consensus.New(g, true, filepath.Join(dir, "consensus"))
	err = handleAsyncErr(errChan)
	if err != nil {
		return err
	}
	tp, err := transactionpool.New(cs, g, filepath.Join(dir, "tpool"))
	if err != nil {
		return err
	}

	store, err := wallet.NewBoltDBStore(filepath.Join(dir, "wallet.db"), nil)
	if err != nil {
		return err
	}
	w := wallet.New(store)
	err = cs.ConsensusSetSubscribe(w.ConsensusSetSubscriber(store), store.ConsensusChangeID(), nil)
	if err != nil {
		return err
	}
	ss := walrus.NewServer(w, tp)

	log.Printf("Listening on %v...", APIaddr)
	return http.ListenAndServe(APIaddr, ss)
}

func reset(dir string) error {
	store, err := wallet.NewBoltDBStore(filepath.Join(dir, "wallet.db"), nil)
	if err != nil {
		return err
	}
	return store.Reset()
}

func handleAsyncErr(errCh <-chan error) error {
	select {
	case err := <-errCh:
		return err
	default:
	}
	go func() {
		err := <-errCh
		if err != nil {
			log.Println("WARNING: consensus initialization returned an error:", err)
		}
	}()
	return nil
}
