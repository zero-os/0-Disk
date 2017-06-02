package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zero-os/0-Disk/zerodisk/cmd/config"
)

// RootCmd represents the base command when called without any subcommands
var RootCmd = &cobra.Command{
	Use: "g8stor",
	Long: `g8stor controls the g8os resources

Find more information at github.com/zero-os/0-Disk/g8stor.`,
}

// Execute adds all child commands to the root command sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(-1)
	}
}

func init() {
	RootCmd.AddCommand(
		VersionCmd,
		CopyCmd,
		DeleteCmd,
		RestoreCmd,
		ListCmd,
	)

	RootCmd.PersistentFlags().BoolVarP(
		&config.Verbose, "verbose", "v",
		false, "log available information")
}