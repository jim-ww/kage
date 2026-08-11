package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/daemon"
	"github.com/jim-ww/kage/xmpp"
	"github.com/spf13/cobra"
)

// newDaemonCmd groups controls over kage's background service (the one
// process per user session that owns every account's XMPP connection,
// storage, and decryption — see daemon.Run). cfgPath/debug/debugXML are
// the root command's persistent flag values.
func newDaemonCmd(cfgPath *string, debug *bool, debugXML *bool) *cobra.Command {
	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "Control kage's background service",
	}

	daemonCmd.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start the background service if it isn't already running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := daemon.EnsureRunning(*cfgPath, *debug, *debugXML); err != nil {
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
			return runDaemonProcess(*cfgPath, *debug, *debugXML)
		},
	}
	daemonCmd.AddCommand(runCmd)

	return daemonCmd
}

// setupXMLLog opens <config dir>/kage/xml.log and points package xmpp's
// wire-level stanza logger at it — off by default since it captures full
// message content, only for diagnosing interop issues with other clients
// (e.g. why a peer client won't render a <reply/> kage sent).
func setupXMLLog(debugXML bool) {
	if !debugXML {
		return
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: determining config dir for xml log: %v\n", err)
		return
	}
	dir = filepath.Join(dir, "kage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "warning: creating %s: %v\n", dir, err)
		return
	}
	path := filepath.Join(dir, "xml.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: opening %s: %v\n", path, err)
		return
	}
	xmpp.SetXMLLog(f)
	slog.Info("xml logging enabled", "log_file", path)
}

// runDaemonProcess is the actual background-service entry point, invoked by
// `kage daemon run` — spawned detached by daemon.EnsureRunning, never meant
// to be typed directly.
func runDaemonProcess(cfgPath string, debug bool, debugXML bool) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	setupLog(debug)
	setupXMLLog(debugXML)
	if cfg.HistoryPageSize > 0 {
		historyPageSize = cfg.HistoryPageSize
	}
	if err := daemon.Run(cfg, newBackend()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return nil
}
