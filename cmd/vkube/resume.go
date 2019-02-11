package main

import (
	"github.com/spf13/cobra"
	"go.universe.tf/virtuakube"
)

var resumeCmd = &cobra.Command{
	Use:   "resume",
	Short: "Resume the universe with no other changes",
	Args:  cobra.NoArgs,
	Run:   withUniverse(&resumeFlags, resume),
}

var resumeFlags universeFlags

func init() {
	rootCmd.AddCommand(resumeCmd)
	addUniverseFlags(resumeCmd, &resumeFlags, true, false)
}

func resume(u *virtuakube.Universe) error {
	return nil
}
