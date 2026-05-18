package main

import (
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"codeberg.org/jim-ww/kage/internal/config"
	"codeberg.org/jim-ww/kage/internal/ui"
)

func main() {
	cfgPath := flag.String("c", "", "path to config")
	flag.Parse()
	uiCfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	model := ui.New(accounts, uiCfg.KeyMap, uiCfg.Theme)
	p := tea.NewProgram(model)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
