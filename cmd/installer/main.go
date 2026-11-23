// Command installer is the guided installation's local server: it runs in the
// live system booted from the Hearthrack ISO, serves the screens on loopback,
// and hands the autoinstall document the screens produce to subiquity.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Plume-Labs/frame/internal/installer"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8099", "loopback address to serve on")
	out := flag.String("autoinstall", "/autoinstall.yaml", "where the autoinstall document is written")
	restart := flag.Bool("restart-subiquity", true, "restart subiquity after writing, so it starts non-interactively (--restart-subiquity=false for a dry run)")
	root := flag.String("root", "/", "filesystem root to read the hardware from (tests)")
	flag.Parse()

	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	}
	src := installer.Source{Root: *root, Run: run}
	srv := &installer.Server{
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
	fmt.Fprintf(os.Stderr, "installer: serving on http://%s\n", ln.Addr())
	s := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(s.Serve(ln))
}

// writeAtomic writes 0600: the document is not secret, but nothing needs it
// readable by anyone else.
func writeAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".autoinstall-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
