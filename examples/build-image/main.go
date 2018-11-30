package main

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"go.universe.tf/virtuakube"
)

func main() {
	wd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	cfg := &virtuakube.BuildConfig{
		OutputPath: filepath.Join(wd, "out.qcow2"),
		TempDir:    wd,
		BuildLog:   os.Stdout,
	}
	if err := virtuakube.BuildK8sImage(context.Background(), cfg); err != nil {
		log.Fatal(err)
	}
}
