package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/signal"
	"time"

	"go.universe.tf/virtuakube"
)

var (
	dir     = flag.String("universe-dir", "", "directory in which to place the universe")
	baseImg = flag.String("vm-img", "virtuakube.qcow2", "VM base image")
	memory  = flag.Int("memory", 1024, "amount of memory per VM, in MiB")
	display = flag.Bool("display", false, "create display windows for each VM")
	verbose = flag.Bool("verbose", false, "show commands being executed during cluster startup")
	kvm     = flag.Bool("kvm", true, "use KVM hardware acceleration")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	start := time.Now()

	universeDir := *dir
	if universeDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		tmp, err := ioutil.TempDir(wd, "vkube")
		if err != nil {
			return err
		}
		universeDir = tmp
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle ctrl+C by cancelling the context, which will shut down
	// everything in the universe.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	fmt.Println("Creating universe...")

	universe, err := virtuakube.New(ctx, universeDir)
	if err != nil {
		return fmt.Errorf("Creating universe: %v", err)
	}
	defer universe.Close()

	cfg := &virtuakube.VMConfig{
		Image:     *baseImg,
		Hostname:  "example",
		MemoryMiB: *memory,
		NoKVM:     !*kvm,
	}
	if *verbose {
		cfg.CommandLog = os.Stdout
	}

	fmt.Println("Creating VM...")

	vm, err := universe.NewVM(cfg)
	if err != nil {
		return fmt.Errorf("Creating VM: %v", err)
	}
	if err = vm.Start(); err != nil {
		return fmt.Errorf("Starting VM: %v", err)
	}

	fmt.Printf(`VM created in %s. SSH access (password is "root"):

ssh -p%d root@localhost

Hit ctrl+C to shut down.
`, time.Since(start).Truncate(time.Millisecond), vm.ForwardedPort(22))

	if err := universe.Wait(context.Background()); err != nil {
		return fmt.Errorf("waiting for universe to end: %v", err)
	}

	fmt.Println("Shutting down...")

	return nil
}
