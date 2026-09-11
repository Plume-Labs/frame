package provision

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// imageName is the shape of a token this package ever produces: 32 hex
// characters from newToken, plus ".iso". It is matched before a caller-
// supplied name ever touches the filesystem, so a name that cannot be an
// image name is refused outright -- not cleaned, not joined-and-hoped-about.
// filepath.Join(dir, name) on an unchecked name is exactly how a media
// server becomes a way to read /etc/shadow.
var imageName = regexp.MustCompile(`^[a-f0-9]{32}\.iso$`)

// preseedName is imageName's counterpart for the preseed route: the same
// 32-hex token, ".cfg" instead of ".iso". Matched before a caller-supplied
// name ever touches the filesystem, for the same reason imageName is.
var preseedName = regexp.MustCompile(`^[a-f0-9]{32}\.cfg$`)

// buildResponse is what the build API answers with. Only the token matters
// to a caller: HTTPImageStore derives the URL the BMC will use itself, from
// MediaURL, rather than trusting a URL the build API might hand back for its
// own (unreachable-by-the-BMC) address.
type buildResponse struct {
	Token string `json:"token"`
}

// newToken generates the per-image identifier: 16 random bytes, hex-encoded
// to the 32 lowercase hex characters imageName requires.
func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating image token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// MediaHandler is the only listener a BMC reaches. It reads, and that is all
// it can do: the build API lives on a different port, on a Service nothing
// outside the cluster can reach.
//
// GET /iso/{name} and GET /preseed/{name} are both single-segment wildcards:
// neither matches a path with an extra segment (e.g. "/iso/sub/dir.iso"),
// and any other method on either path gets 405 from http.ServeMux itself,
// not from any check written here. A trailing-slash pattern is never used
// -- see the package-level note this lot has already paid a task to learn:
// in http.ServeMux a trailing-slash pattern is a subtree match that absorbs
// every deeper path, which is exactly how a predecessor lot's 404 tolerance
// went untested.
//
// /preseed/{name} is exposed on the same read-only, unauthenticated
// listener as /iso/{name}: the preseed carries no secret by construction --
// RenderPreseed refuses private key material and anything shell-unsafe
// before an image is ever built -- so serving it to the machine network is
// the same risk as serving the image it came from.
func MediaHandler(dir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /iso/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		// Matched before it touches the filesystem. A name that cannot be an
		// image name is refused outright, rather than cleaned and hoped
		// about.
		if !imageName.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, name))
	})
	mux.HandleFunc("GET /preseed/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !preseedName.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, name))
	})
	return mux
}

// BuildHandler serves the build API. It must never be exposed outside the
// cluster: it accepts a Spec and writes a 700 MB file, and doing either of
// those on the LAN-facing listener is the mistake the two-listener design
// exists to make impossible.
//
// It decodes a Spec, calls RenderPreseed first -- so a bad spec is a 400
// before anything is fetched or built -- then fetches the base image,
// writes the rendered preseed to dir/<token>.cfg, and remasters the image
// into dir/<token>.iso with boot arguments that fetch that same file from
// mediaURL, the base address of the read-only listener MediaHandler serves
// (the one the BMC reaches, not this one). token is 32 hex characters from
// crypto/rand. DELETE /iso/{name} removes a previously built image and its
// preseed together, guarded by the same imageName check as the read-only
// listener.
func BuildHandler(dir string, base BaseSource, mediaURL string) http.Handler {
	mux := http.NewServeMux()
	baseDir := filepath.Join(dir, "base")

	mux.HandleFunc("POST /build", func(w http.ResponseWriter, r *http.Request) {
		var spec Spec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			http.Error(w, fmt.Sprintf("decoding request body: %v", err), http.StatusBadRequest)
			return
		}

		// Rendered before anything is fetched or built: a spec this package
		// would refuse to turn into a preseed is refused here too, before a
		// single byte of a ~700 MB download happens. The rendered content is
		// kept, rather than discarded and re-rendered later, so what gets
		// written to disk and what was just validated are provably the same
		// call's output.
		preseed, err := RenderPreseed(spec)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		baseISO, err := FetchBase(r.Context(), baseDir, base)
		if err != nil {
			http.Error(w, fmt.Sprintf("fetching base image: %v", err), http.StatusInternalServerError)
			return
		}

		token, err := newToken()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			http.Error(w, fmt.Sprintf("preparing image directory: %v", err), http.StatusInternalServerError)
			return
		}

		cfgPath := filepath.Join(dir, token+".cfg")
		if err := os.WriteFile(cfgPath, []byte(preseed), 0o644); err != nil {
			http.Error(w, fmt.Sprintf("writing preseed: %v", err), http.StatusInternalServerError)
			return
		}

		// The URL baked into the image's boot arguments and the one
		// MediaHandler's /preseed/{name} route actually serves must be the
		// same address by construction, not by coincidence -- both are
		// built from mediaURL and token here, in one place.
		preseedURL := strings.TrimSuffix(mediaURL, "/") + "/preseed/" + token + ".cfg"

		out := filepath.Join(dir, token+".iso")
		if err := Remaster(r.Context(), baseISO, spec, preseedURL, out); err != nil {
			http.Error(w, fmt.Sprintf("building image: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildResponse{Token: token})
	})

	mux.HandleFunc("DELETE /iso/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !imageName.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			if os.IsNotExist(err) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Best effort: the .iso removal above is the one a caller's retry
		// depends on, and a missing .cfg (say, a prior cleanup that got this
		// far and no further) must not turn a successful image removal into
		// a 500.
		token := strings.TrimSuffix(name, ".iso")
		if err := os.Remove(filepath.Join(dir, token+".cfg")); err != nil && !os.IsNotExist(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	return mux
}

// HTTPImageStore is the ImageStore the controller uses, over the build API.
//
// Build talks to BuildURL, the in-cluster address the manager reaches over
// the ClusterIP Service. The URL it returns is built from MediaURL instead
// -- the address the BMC, on the management network, will fetch from -- so
// the two listeners this design deliberately keeps separate are never
// confused with each other.
type HTTPImageStore struct {
	BuildURL string // the in-cluster build API
	MediaURL string // the base URL the BMC will fetch from
	Client   *http.Client
}

func (s *HTTPImageStore) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func (s *HTTPImageStore) Build(ctx context.Context, spec Spec) (url, token string, err error) {
	body, err := json.Marshal(spec)
	if err != nil {
		return "", "", fmt.Errorf("encoding spec: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(s.BuildURL, "/")+"/build", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client().Do(req)
	if err != nil {
		return "", "", fmt.Errorf("posting build request to %s: %w", s.BuildURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("build %s: HTTP %d: %s", s.BuildURL, resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var out buildResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", fmt.Errorf("decoding build response: %w", err)
	}
	// The response feeds directly into a URL handed to a BMC, unauthenticated,
	// on the management network. A build API that answered with a malformed
	// token would otherwise publish whatever that string is, verbatim.
	if !imageName.MatchString(out.Token + ".iso") {
		return "", "", fmt.Errorf("build %s: response token %q is not a valid image token", s.BuildURL, out.Token)
	}

	return strings.TrimSuffix(s.MediaURL, "/") + "/iso/" + out.Token + ".iso", out.Token, nil
}

func (s *HTTPImageStore) Remove(ctx context.Context, token string) error {
	if !imageName.MatchString(token + ".iso") {
		return fmt.Errorf("token %q is not a valid image token", token)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimSuffix(s.BuildURL, "/")+"/iso/"+token+".iso", nil)
	if err != nil {
		return err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("posting remove request to %s: %w", s.BuildURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("remove %s: HTTP %d: %s", token, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// LocalImageStore is the ImageStore `frame bootstrap` uses: it calls
// FetchBase and Remaster directly, because when no cluster exists there is
// nothing to host a build API.
type LocalImageStore struct {
	Dir      string
	Base     BaseSource
	MediaURL string // how the BMC reaches this machine, e.g. http://192.168.2.50:8081
}

func (s *LocalImageStore) Build(ctx context.Context, spec Spec) (url, token string, err error) {
	// Same order as BuildHandler, for the same reason: refuse before
	// fetching or building anything. The rendered content is kept for the
	// same reason too -- what is written to dir/<token>.cfg is provably
	// this call's output, not a second, possibly-diverged render.
	preseed, err := RenderPreseed(spec)
	if err != nil {
		return "", "", err
	}

	baseISO, err := FetchBase(ctx, filepath.Join(s.Dir, "base"), s.Base)
	if err != nil {
		return "", "", fmt.Errorf("fetching base image: %w", err)
	}

	tok, err := newToken()
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return "", "", fmt.Errorf("preparing image directory: %w", err)
	}

	if err := os.WriteFile(filepath.Join(s.Dir, tok+".cfg"), []byte(preseed), 0o644); err != nil {
		return "", "", fmt.Errorf("writing preseed: %w", err)
	}

	// Built from the same MediaURL the returned image URL below is, so the
	// address baked into the image's boot arguments is the one this store
	// actually serves -- see BuildHandler's identical construction.
	preseedURL := strings.TrimSuffix(s.MediaURL, "/") + "/preseed/" + tok + ".cfg"

	out := filepath.Join(s.Dir, tok+".iso")
	if err := Remaster(ctx, baseISO, spec, preseedURL, out); err != nil {
		return "", "", fmt.Errorf("building image: %w", err)
	}

	return strings.TrimSuffix(s.MediaURL, "/") + "/iso/" + tok + ".iso", tok, nil
}

func (s *LocalImageStore) Remove(_ context.Context, token string) error {
	if !imageName.MatchString(token + ".iso") {
		return fmt.Errorf("token %q is not a valid image token", token)
	}
	if err := os.Remove(filepath.Join(s.Dir, token+".iso")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(filepath.Join(s.Dir, token+".cfg")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
