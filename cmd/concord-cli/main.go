package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// rootCmd builds the top-level cobra command with every subcommand registered.
func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "concord-cli",
		Short: "Concord management CLI (operates directly on the database/cache)",
		Long: "concord-cli is a management tool for Concord. It connects directly to Postgres\n" +
			"and Redis using the app configuration, so commands work while the server runs.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		dbshellCmd(),
		cacheshellCmd(),
		migrateCmd(),
		userCmd(),
		banCmd(),
		unbanCmd(),
		listBansCmd(),
		settingsCmd(),
		statsCmd(),
		healthCmd(),
		voiceServersCmd(),
		purgeMessagesCmd(),
		clearRateLimitCmd(),
	)
	return root
}
