package main

import (
	"fmt"
	"os"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/daemon"
	"github.com/spf13/cobra"
)

// newDaemonCmd groups controls over kage's background service (the one
// process per user session that owns every account's XMPP connection,
// storage, and decryption — see daemon.Run). cfgPath/debug are the root
// command's persistent flag values.
func newDaemonCmd(cfgPath *string, debug *bool) *cobra.Command {
	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "Control kage's background service",
	}

	daemonCmd.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start the background service if it isn't already running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := daemon.EnsureRunning(*cfgPath, *debug); err != nil {
				return err
			}
			fmt.Println("kage background service is running")
			return nil
		},
	})

	daemonCmd.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop the running background service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ok, err := daemon.Stop()
			if err != nil {
				return err
			}
			if !ok {
				fmt.Println("kage background service is not running")
				return nil
			}
			fmt.Println("kage background service stopped")
			return nil
		},
	})

	daemonCmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Report whether the background service is running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			running, pid := daemon.Status()
			if !running {
				fmt.Println("kage background service is not running")
				return nil
			}
			if pid > 0 {
				fmt.Printf("kage background service is running (pid %d)\n", pid)
			} else {
				fmt.Println("kage background service is running")
			}
			return nil
		},
	})

	runCmd := &cobra.Command{
		Use:    "run",
		Short:  "Run as the background service (internal — spawned automatically by `daemon start`, not meant to be run by hand)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDaemonProcess(*cfgPath, *debug)
		},
	}
	daemonCmd.AddCommand(runCmd)

	return daemonCmd
}

// runDaemonProcess is the actual background-service entry point, invoked by
// `kage daemon run` — spawned detached by daemon.EnsureRunning, never meant
// to be typed directly.
func runDaemonProcess(cfgPath string, debug bool) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	setupLog(debug)
	if cfg.HistoryPageSize > 0 {
		historyPageSize = cfg.HistoryPageSize
	}
	if err := daemon.Run(cfg, newBackend()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return nil
}
