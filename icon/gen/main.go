// Command gen writes kage's icon to disk: the SVG variants and the plate
// PNGs that README and desktop-entry packaging reference as files, plus the
// tray PNGs as reference artifacts (the daemon renders those itself — see
// icon.Tray — so nothing in the binary reads them).
//
// Run it via `go generate ./icon`; the output is committed, and
// icon's TestAssetsMatchGenerated fails if it drifts.
package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/jim-ww/kage/icon"
)

func main() {
	out := "assets/icon"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	assets := icon.Assets()
	for _, name := range slices.Sorted(maps.Keys(assets)) {
		path := filepath.Join(out, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fail(err)
		}
		if err := os.WriteFile(path, assets[name], 0o644); err != nil {
			fail(err)
		}
		fmt.Println(path)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "icon/gen:", err)
	os.Exit(1)
}
