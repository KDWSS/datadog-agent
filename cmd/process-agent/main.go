// +build !windows

package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/DataDog/datadog-agent/cmd/process-agent/flags"
)

func rootCmdRun(cmd *cobra.Command, args []string) {
	exit := make(chan struct{})

	// Invoke the Agent
	runAgent(exit)
}

func main() {
	ignore := ""
	rootCmd.PersistentFlags().StringVar(&opts.configPath, "config", flags.DefaultConfPath, "Path to datadog.yaml config")
	rootCmd.PersistentFlags().StringVar(&ignore, "ddconfig", "", "[deprecated] Path to dd-agent config")

	if flags.DefaultSysProbeConfPath != "" {
		rootCmd.PersistentFlags().StringVar(&opts.sysProbeConfigPath, "sysprobe-config", flags.DefaultSysProbeConfPath, "Path to system-probe.yaml config")
	}

	rootCmd.PersistentFlags().StringVarP(&opts.pidfilePath, "pid", "p", "", "Path to set pidfile for process")
	rootCmd.PersistentFlags().BoolVarP(&opts.info, "info", "i", false, "Show info about running process agent and exit")
	rootCmd.PersistentFlags().BoolVarP(&opts.version, "version", "v", false, "Print the version and exit")
	rootCmd.PersistentFlags().StringVar(&opts.check, "check", "",
		"Run a specific check and print the results. Choose from: process, connections, realtime, process_discovery")

	fixDeprecatedFlags()
	if err := rootCmd.Execute(); err != nil {
		os.Exit(-1)
	}
}
