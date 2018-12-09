package main

import (
	"github.com/spf13/cobra"
	"go.universe.tf/virtuakube"
)

var resumeCmd = &cobra.Command{
	Use:   "resume",
	Short: "Create a standalone VM",
	Args:  cobra.NoArgs,
	Run:   withUniverse(resume),
}

func init() {
	rootCmd.AddCommand(resumeCmd)
	addRootFlags(resumeCmd, true, false)
}

func resume(u *virtuakube.Universe) error {
	return nil
}
