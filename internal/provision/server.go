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
	"net/url"
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

// runScriptName is the third name this package produces: the same 32-hex
// token, ".sh". It is the preseed/run script (NetcfgRerunScript) the
// rendered preseed points d-i at, and it is served from the SAME route as
// the preseed, deliberately -- d-i resolves a relative preseed/run value
// against the directory the preconfiguration file came from, and Frame
// writes an absolute URL. Putting the two files on one route means both
// readings land on the same file, so which one d-i actually does is not a
// question this has to get right.
var runScriptName = regexp.MustCompile(`^[a-f0-9]{32}\.sh$`)

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
//
// One thing in it is not nothing, and is named rather than glossed: the
// install UID. It is the marker WaitForOurSystem checks, and moving the
// preseed off the image put it on the wire in plaintext. It is 16 random
// bytes behind an unguessable 32-hex path, valid for one installation and
// meaningless after it -- so what it protects against is an unrelated
// machine happening to answer at the target address, not an attacker who
// can read this network. Design §7 says the same, and used to say the
// stronger thing this route made untrue.
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
		// Two guards, not one pattern with an alternation: each refuses on
		// its own, so a mutation to either is visible as exactly one test
		// going red rather than as a combined pattern that still matches
		// half of what it used to.
		if !preseedName.MatchString(name) && !runScriptName.MatchString(name) {
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

		// Validated before anything is fetched or built: a spec this package
		// would refuse to turn into a preseed is refused here too, before a
		// single byte of a ~700 MB download happens. The render itself has
		// to wait for the token, because the preseed now names the
		// preseed/run script by its token-derived URL -- so the refusals
		// are split out into ValidateSpec rather than duplicated, and there
		// is still exactly one render, whose output is what lands on disk.
		if err := ValidateSpec(spec); err != nil {
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

		// The URL baked into the image's boot arguments and the one
		// MediaHandler's /preseed/{name} route actually serves must be the
		// same address by construction, not by coincidence -- all three are
		// built from mediaURL and token here, in one place.
		preseedURL := strings.TrimSuffix(mediaURL, "/") + "/preseed/" + token + ".cfg"
		runURL := strings.TrimSuffix(mediaURL, "/") + "/preseed/" + token + ".sh"

		preseed, err := RenderPreseed(spec, runURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		cfgPath := filepath.Join(dir, token+".cfg")
		if err := os.WriteFile(cfgPath, []byte(preseed), 0o644); err != nil {
			http.Error(w, fmt.Sprintf("writing preseed: %v", err), http.StatusInternalServerError)
			return
		}
		// Written beside the preseed, on the same route, read-only. Without
		// it the preseed's preseed/run directive names a 404 and netcfg
		// never re-runs -- which is indistinguishable, from here, from the
		// install simply taking a long time.
		if err := os.WriteFile(filepath.Join(dir, token+".sh"), []byte(NetcfgRerunScript), 0o644); err != nil {
			http.Error(w, fmt.Sprintf("writing preseed/run script: %v", err), http.StatusInternalServerError)
			return
		}

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
		// depends on, and a missing .cfg or .sh (say, a prior cleanup that
		// got this far and no further) must not turn a successful image
		// removal into a 500.
		token := strings.TrimSuffix(name, ".iso")
		for _, side := range []string{token + ".cfg", token + ".sh"} {
			if err := os.Remove(filepath.Join(dir, side)); err != nil && !os.IsNotExist(err) {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
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
//
// It sends a Spec with Cluster zeroed: the build side does not use it, and
// it carries the k3s join token. See Build.
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
	// The k3s join token never leaves this process. It was being POSTed in
	// cleartext to an unauthenticated in-cluster build API that has no use
	// for it: RenderPreseed does not read Spec.Cluster at all, so nothing
	// downstream of this request ever looks at it. Decision 3 of the design
	// rejected serving a cluster-membership token from an in-cluster HTTP
	// endpoint on the grounds that every notebook and every sandbox on the
	// platform can reach one -- and then this sent it there anyway.
	//
	// Zeroed rather than nulled field-by-field, so a future field added to
	// ClusterTarget is excluded by default instead of leaking until someone
	// remembers this line.
	spec.Cluster = ClusterTarget{}

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

// ValidateMediaURL refuses anything that is not a usable http(s) base URL a
// BMC on the management network could fetch from.
//
// Checking presence alone only catches a missing value; it says nothing
// about one that is wrong in a way that shows up only on hardware later --
// a typo'd scheme (ftp://, or http:/ missing a slash), or a bare hostname
// with no scheme, which url.Parse accepts without error and puts entirely
// into Path, leaving Scheme and Host both empty. Every one of those builds
// an image whose boot arguments point nowhere and fails twenty minutes in,
// with nothing saying why.
//
// It lives here, beside the code that builds URLs from this value, so the
// manager, frame-provisiond and the FrameInstall controller check the same
// thing rather than three copies of it. Callers wrap the error with the
// name of whatever they call this setting.
func ValidateMediaURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("is not set: every built image bakes this address into its own boot arguments, so an image built against it would fetch its preseed from nowhere")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%q: scheme must be http or https, got %q", raw, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%q: has no host", raw)
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
	// fetching or building anything. The render itself waits for the token,
	// because the preseed names its preseed/run script by a token-derived
	// URL -- so the refusals run here and the single render runs below.
	if err := ValidateSpec(spec); err != nil {
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

	// Built from the same MediaURL the returned image URL below is, so the
	// address baked into the image's boot arguments is the one this store
	// actually serves -- see BuildHandler's identical construction.
	preseedURL := strings.TrimSuffix(s.MediaURL, "/") + "/preseed/" + tok + ".cfg"
	runURL := strings.TrimSuffix(s.MediaURL, "/") + "/preseed/" + tok + ".sh"

	preseed, err := RenderPreseed(spec, runURL)
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(s.Dir, tok+".cfg"), []byte(preseed), 0o644); err != nil {
		return "", "", fmt.Errorf("writing preseed: %w", err)
	}
	// Beside the preseed, on the same read-only route -- see BuildHandler.
	if err := os.WriteFile(filepath.Join(s.Dir, tok+".sh"), []byte(NetcfgRerunScript), 0o644); err != nil {
		return "", "", fmt.Errorf("writing preseed/run script: %w", err)
	}

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
	for _, side := range []string{token + ".cfg", token + ".sh"} {
		if err := os.Remove(filepath.Join(s.Dir, side)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
