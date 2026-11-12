package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jim-ww/kage/ui"
)

func TestAutofocusValues(t *testing.T) {
	for _, v := range []string{"own", "others", "all"} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("autofocus = \""+v+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := loadConfigFile(path)
		if err != nil || cfg.Autofocus != ui.Autofocus(v) {
			t.Fatalf("autofocus = %q: got %q, err %v", v, cfg.Autofocus, err)
		}
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("autofocus = \"self\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadConfigFile(path); err == nil || !strings.Contains(err.Error(), `"own", "others" or "all"`) {
		t.Fatalf("autofocus = \"self\": err = %v, want one naming the valid values", err)
	}
}
