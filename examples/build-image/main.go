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
		InstallScript: []byte(`echo "test script run"
`),
		OutputPath: filepath.Join(wd, "out.qcow2"),
		TempDir:    wd,
		Debug:      true,
	}
	if err := virtuakube.BuildImage(context.Background(), cfg); err != nil {
		log.Fatal(err)
	}
}
