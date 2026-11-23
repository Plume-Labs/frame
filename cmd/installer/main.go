// Command installer is the guided installation's local server: it runs in the
// live system booted from the Hearthrack ISO, serves the screens on loopback,
// and hands the autoinstall document the screens produce to subiquity.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Plume-Labs/frame/internal/installer"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8099", "loopback address to serve on")
	out := flag.String("autoinstall", "/autoinstall.yaml", "where the autoinstall document is written")
	restart := flag.Bool("restart-subiquity", true,
		"restart subiquity after writing, so it starts non-interactively (false: dry run)")
	consoleTTY := flag.String("console", "",
		"also run the text assistant on this terminal (e.g. /dev/tty8), for screens that cannot show a page")
	root := flag.String("root", "/", "filesystem root to read the hardware from (tests)")
	flag.Parse()

	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	}
	src := installer.Source{Root: *root, Run: run}
	srv := &installer.Server{
		Token:    newToken(),
		Keymap:   loadKeys,
		Hardware: func(ctx context.Context) (installer.Hardware, error) { return installer.Detect(ctx, src) },
		Start: func(ctx context.Context, doc string) error {
			if err := writeAtomic(*out, []byte(doc)); err != nil {
				return err
			}
			if !*restart {
				return nil
			}
			// cloud-init of the live system must be done first (spike S0).
			_ = exec.CommandContext(ctx, "cloud-init", "status", "--wait").Run()
			return exec.CommandContext(ctx, "systemctl", "restart", "snap.subiquity.subiquity-server").Run()
		},
	}
	ln, err := installer.ListenLoopback(*listen)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "installer: open http://%s/#%s\n", ln.Addr(), srv.Token)
	if *consoleTTY != "" {
		go runConsole(*consoleTTY, srv)
	}
	s := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute}
	log.Fatal(s.Serve(ln))
}

func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// writeAtomic writes 0600: the document is not secret, but nothing needs it
// readable by anyone else.
func writeAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".autoinstall-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// runConsole puts the text assistant on a terminal of its own, so it does not
// fight the installer's interface for tty1.
func runConsole(path string, srv *installer.Server) {
	tty, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		log.Printf("installer: text console %s: %v", path, err)
		return
	}
	defer func() { _ = tty.Close() }()
	// Switch the screen only now that the terminal is ours: a switch to a terminal
	// nothing serves would leave a black screen and hide the installer's own.
	if n, ok := strings.CutPrefix(path, "/dev/tty"); ok {
		_ = exec.Command("chvt", n).Run()
	}
	if err := installer.RunConsole(context.Background(), tty, tty, srv); err != nil {
		log.Printf("installer: text console: %v", err)
	}
}

// consoleKeymaps names, for each layout the installer offers, the keymap of the
// kbd package's loadkeys.
var consoleKeymaps = map[string]string{
	"us": "us", "fr": "fr", "be": "be-latin1", "gb": "uk", "de": "de", "es": "es", "it": "it",
}

// loadKeys switches the live console's keyboard layout.
func loadKeys(layout string) error {
	name, ok := consoleKeymaps[layout]
	if !ok {
		return fmt.Errorf("no console keymap for layout %q", layout)
	}
	return exec.Command("loadkeys", name).Run()
}
