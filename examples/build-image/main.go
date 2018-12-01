package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"

	"go.universe.tf/virtuakube"
)

var (
	kvm = flag.Bool("kvm", true, "use KVM hardware acceleration")
)

func main() {
	flag.Parse()

	wd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	cfg := &virtuakube.BuildConfig{
		OutputPath: filepath.Join(wd, "out.qcow2"),
		TempDir:    wd,
		CustomizeFuncs: []virtuakube.BuildCustomizeFunc{
			virtuakube.CustomizeInstallK8s,
			virtuakube.CustomizePreloadK8sImages,
		},
		BuildLog: os.Stdout,
		NoKVM:    !*kvm,
	}
	if err := virtuakube.BuildImage(context.Background(), cfg); err != nil {
		log.Fatal(err)
	}
}
