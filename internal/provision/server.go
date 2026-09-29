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
	"time"
)

// imageName is the shape of a token this package ever produces: 32 hex
// characters from newToken, plus ".iso". It is matched before a caller-
// supplied name ever touches the filesystem, so a name that cannot be an
// image name is refused outright -- not cleaned, not joined-and-hoped-about.
// filepath.Join(dir, name) on an unchecked name is exactly how a media
// server becomes a way to read /etc/shadow.
var imageName = regexp.MustCompile(`^[a-f0-9]{32}\.iso$`)

// seedFiles is the closed list of names served under /autoinstall/{token}/.
// cloud-init's NoCloud datasource fetches user-data and meta-data, and asks
// for vendor-data too; the last two are empty (NoCloud supplies a default
// instance-id). Any other name is a 404, before the filesystem is touched.
var seedFiles = map[string]bool{"user-data": true, "meta-data": true, "vendor-data": true}

// seedURL is the one place the NoCloud seed address is built, so the address
// baked into an image's boot arguments and the route MediaHandler serves
// cannot drift apart. The trailing slash is required: cloud-init appends the
// file names to it.
func seedURL(mediaURL, token string) string {
	return strings.TrimSuffix(mediaURL, "/") + "/autoinstall/" + token + "/"
}

// writeSeed renders the autoinstall document for spec and writes the three
// seed files under dir/<token>/. Shared by BuildHandler and LocalImageStore so
// the two stores cannot write different layouts.
func writeSeed(dir, token string, spec Spec, beaconBase string) error {
	doc, err := RenderAutoinstall(spec, beaconBase, token)
	if err != nil {
		return err
	}
	seedDir := filepath.Join(dir, token)
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		return fmt.Errorf("preparing seed directory: %w", err)
	}
	for name, content := range map[string]string{"user-data": doc, "meta-data": "", "vendor-data": ""} {
		if err := os.WriteFile(filepath.Join(seedDir, name), []byte(content), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}
	return nil
}

// removeSeed deletes a token's seed directory. Best effort on absence: a
// prior cleanup that got this far must not turn a later one into an error.
func removeSeed(dir, token string) error {
	if !beaconToken.MatchString(token) {
		return fmt.Errorf("token %q is not a valid image token", token)
	}
	if err := os.RemoveAll(filepath.Join(dir, token)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// discardBuild removes what a failed build left under dir: the seed, whole or
// partial, and any partial image. Best effort -- the build's own error
// is what the caller hears about.
func discardBuild(dir, token string) {
	_ = removeSeed(dir, token)
	_ = os.Remove(filepath.Join(dir, token+".iso"))
}

// buildResponse is what the build API answers with. Only the token matters
// to a caller: HTTPImageStore derives the URL the BMC will use itself, from
// MediaURL, rather than trusting a URL the build API might hand back for its
// own (unreachable-by-the-BMC) address.
type buildResponse struct {
	Token string `json:"token"`
}

// newToken generates the per-image identifier: 16 random bytes, hex-encoded
// to the 32 lowercase hex characters imageName requires. A variable so a test
// can name the token and plant what makes writeSeed fail under it.
var newToken = func() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating image token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// MediaHandler is the only listener a BMC reaches. It is not read-only: it
// may deposit an installation's progress (the beacon route below), but it
// may never disclose one -- the build API, on a different port and a
// Service nothing outside the cluster can reach, is the only listener
// that reads beacon state back (BuildHandler). Write-only is not the same
// guarantee as read-only, and it holds here because the write stores
// nothing a caller chose: BeaconStore.Record refuses any token or
// checkpoint outside its closed shapes before either reaches memory, and
// the route itself returns nothing but a status code.
//
// GET /iso/{name} is a single-segment wildcard, and GET
// /autoinstall/{token}/{file} and GET /beacon/{token}/{checkpoint} are
// two-segment ones:
// none matches a path with an extra segment (e.g. "/iso/sub/dir.iso"),
// and any other method on any of the three gets 405 from http.ServeMux
// itself, not from any check written here. A trailing-slash pattern is
// never used -- see the package-level note this lot has already paid a
// task to learn: in http.ServeMux a trailing-slash pattern is a subtree
// match that absorbs every deeper path, which is exactly how a
// predecessor lot's 404 tolerance went untested.
//
// /autoinstall/{token}/{file} is exposed on the same read-only,
// unauthenticated listener as /iso/{name}: the seed carries no secret by
// construction -- RenderAutoinstall refuses private key material and
// anything shell-unsafe before an image is ever built -- so serving it to the
// machine network is the same risk as serving the image it came from.
//
// One thing in it is not nothing, and is named rather than glossed: the
// install UID. It is the marker WaitForOurSystem checks, and moving the
// configuration off the image put it on the wire in plaintext. It is 16 random
// bytes behind an unguessable 32-hex path, valid for one installation and
// meaningless after it -- so what it protects against is an unrelated
// machine happening to answer at the target address, not an attacker who
// can read this network. Design §7 says the same, and used to say the
// stronger thing this route made untrue.
func MediaHandler(dir string, beacons *BeaconStore) http.Handler {
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
	mux.HandleFunc("GET /autoinstall/{token}/{file}", func(w http.ResponseWriter, r *http.Request) {
		token, file := r.PathValue("token"), r.PathValue("file")
		// Two guards, each refusing on its own, before the filesystem is
		// touched: a token of the one shape newToken produces, and a file
		// from the closed list.
		if !beaconToken.MatchString(token) || !seedFiles[file] {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, token, file))
	})

	// The write half of the beacon mechanism, and the only half that lives
	// here. Reads are on the build listener (BuildHandler): this listener is
	// reachable by anything on the management network, so it may deposit an
	// installation's progress and may never disclose it.
	//
	// Registered only when a store exists. A route that accepted beacons and
	// dropped them would be worse than no route: the machine's sends would
	// succeed and the absence of any record would read as "the installer
	// never spoke".
	//
	// Two path segments, both wildcards, both matched against a closed shape
	// before anything is stored -- the same rule /iso/{name} follows. The
	// response is 204 with no body: the machine ignores it, and there is
	// nothing to say.
	if beacons != nil {
		mux.HandleFunc("GET /beacon/{token}/{checkpoint}", func(w http.ResponseWriter, r *http.Request) {
			token := r.PathValue("token")
			// Bounds the store to real installations, checked before
			// Record is ever called. Record shape-checks a token (32 hex
			// characters) but has no way to know whether it names an
			// installation that exists -- every well-formed token is
			// otherwise just as insertable as a real one, and eviction is
			// oldest-inserted, so enough of them evict a live install's own
			// entry. os.Stat costs one syscall and opens no new oracle:
			// /iso/{name} on this same listener already discloses whether
			// an image exists. The check belongs here, not in
			// BeaconStore: the store has no business knowing about a
			// filesystem.
			//
			// beaconToken is matched first so a token that cannot be a
			// path element (e.g. containing "..") never reaches
			// filepath.Join -- the same discipline imageName gets for
			// /iso/{name} above.
			if !beaconToken.MatchString(token) {
				http.NotFound(w, r)
				return
			}
			if _, err := os.Stat(filepath.Join(dir, token+".iso")); err != nil {
				http.NotFound(w, r)
				return
			}
			if !beacons.Record(token, r.PathValue("checkpoint"), time.Now()) {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	return mux
}

// BuildHandler serves the build API. It must never be exposed outside the
// cluster: it accepts a Spec and writes a ~4 GB file, and doing either of
// those on the LAN-facing listener is the mistake the two-listener design
// exists to make impossible.
//
// It decodes a Spec, calls ValidateSpec first -- so a bad spec is a 400
// before anything is fetched or built -- then fetches the base image,
// writes the NoCloud seed to dir/<token>/, and remasters the image into
// dir/<token>.iso with boot arguments that fetch that seed from mediaURL, the
// base address of the read-only listener MediaHandler serves (the one the BMC
// reaches, not this one). token is 32 hex characters from crypto/rand.
// DELETE /iso/{name} removes a previously built image and its seed together, guarded by the same imageName check as the read-only
// listener, and -- when beacons is non-nil -- forgets that token's beacon
// state too. GET /beacon/{token} is the read half of the beacon mechanism;
// see its registration below for why it lives on this listener and not the
// other one.
func BuildHandler(dir string, base BaseSource, mediaURL string, beacons *BeaconStore) http.Handler {
	mux := http.NewServeMux()
	baseDir := filepath.Join(dir, "base")

	mux.HandleFunc("POST /build", func(w http.ResponseWriter, r *http.Request) {
		var spec Spec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			http.Error(w, fmt.Sprintf("decoding request body: %v", err), http.StatusBadRequest)
			return
		}

		// Validated before anything is fetched or built: a spec this package
		// would refuse to render is refused here too, before a single byte of
		// a ~4 GB download happens. The render itself waits for the token,
		// which the beacon commands carry.
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

		// Built from mediaURL like the seed address, and for the same reason:
		// the address baked into the image and the address this deployment
		// actually serves must be one construction, not two that happen to
		// agree. Empty when no store exists, which renders no beacons rather
		// than commands pointing at a route that is not there.
		beaconBase := mediaURL
		if beacons == nil {
			beaconBase = ""
		}
		if err := writeSeed(dir, token, spec, beaconBase); err != nil {
			// A seed half written is served like a whole one, and the
			// caller gets no token to DELETE it by: same as a failed image.
			discardBuild(dir, token)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		out := filepath.Join(dir, token+".iso")
		if err := Remaster(r.Context(), baseISO, spec, seedURL(mediaURL, token), out); err != nil {
			// The caller gets no token, so nothing will ever DELETE what
			// this build left: the seed would stay served on the LAN-facing
			// listener for the life of the pod. Removed here, with any
			// partial image, or not at all.
			discardBuild(dir, token)
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
		// The seed goes whether or not the image is still there: a failed
		// build or an interrupted cleanup can leave a seed with no image,
		// and the seed is the part the LAN-facing listener serves. 404 only
		// when there was neither, so a retry of a cleanup that already
		// succeeded still reads as "nothing here".
		token := strings.TrimSuffix(name, ".iso")
		_, seedErr := os.Stat(filepath.Join(dir, token))
		isoErr := os.Remove(filepath.Join(dir, name))
		if isoErr != nil && !os.IsNotExist(isoErr) {
			http.Error(w, isoErr.Error(), http.StatusInternalServerError)
			return
		}
		if err := removeSeed(dir, token); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if os.IsNotExist(isoErr) && os.IsNotExist(seedErr) {
			http.NotFound(w, r)
			return
		}
		// Beacon state is dropped with the image it belongs to, so its
		// lifetime cannot outlive the installation it describes.
		if beacons != nil {
			beacons.Forget(token)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// The read half. It lives here and nowhere else: this listener is the
	// in-cluster one, on a Service nothing outside the cluster reaches,
	// which is why it may disclose what the LAN-facing listener collected.
	//
	// A 404 means "nothing has been heard from this installation". The
	// controller maps that to NeverSeen, and maps a failure to reach this
	// endpoint at all to Unavailable -- two different things that must not
	// be collapsed, which is why "no beacons" is a status code rather than
	// a zero-valued 200.
	if beacons != nil {
		mux.HandleFunc("GET /beacon/{token}", func(w http.ResponseWriter, r *http.Request) {
			st, ok := beacons.Get(r.PathValue("token"))
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(st)
		})
	}

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

func (s *HTTPImageStore) Build(ctx context.Context, spec Spec) (imageURL, token string, err error) {
	// The k3s join token never leaves this process. It was being POSTed in
	// cleartext to an unauthenticated in-cluster build API that has no use
	// for it: RenderAutoinstall does not read Spec.Cluster at all, so nothing
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

// ProgressReader is how the controller asks provisiond what an installation
// has reported. It is deliberately NOT part of Deps: provision.Install has
// no business reading beacon state, and the surest way to keep it that way
// is to give it no interface that could.
type ProgressReader interface {
	Progress(ctx context.Context, token string) (BeaconState, bool, error)
}

// Progress reads one installation's beacon state from the build API.
//
// The three outcomes are kept apart on purpose: known state, a 404 meaning
// nothing has been heard from that installation, and an error meaning this
// process could not ask. A caller that collapsed the last two would report
// a silent machine when the truth was a provisiond it could not reach.
func (s *HTTPImageStore) Progress(ctx context.Context, token string) (BeaconState, bool, error) {
	if !beaconToken.MatchString(token) {
		return BeaconState{}, false, fmt.Errorf("token %q is not a valid image token", token)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(s.BuildURL, "/")+"/beacon/"+token, nil)
	if err != nil {
		return BeaconState{}, false, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return BeaconState{}, false, fmt.Errorf("reading progress from %s: %w", s.BuildURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotFound:
		return BeaconState{}, false, nil
	case http.StatusOK:
		var st BeaconState
		if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
			return BeaconState{}, false, fmt.Errorf("decoding progress response: %w", err)
		}
		return st, true, nil
	default:
		b, _ := io.ReadAll(resp.Body)
		return BeaconState{}, false, fmt.Errorf("progress %s: HTTP %d: %s", s.BuildURL, resp.StatusCode, strings.TrimSpace(string(b)))
	}
}

// ValidateMediaURLSyntax refuses anything that is not a well-formed http(s)
// base URL.
//
// Checking presence alone only catches a missing value; it says nothing
// about one that is wrong in a way that shows up only on hardware later --
// a typo'd scheme (ftp://, or http:/ missing a slash), or a bare hostname
// with no scheme, which url.Parse accepts without error and puts entirely
// into Path, leaving Scheme and Host both empty. Every one of those builds
// an image whose boot arguments point nowhere and fails twenty minutes in,
// with nothing saying why.
//
// It is deliberately NOT the whole check -- see ValidateMediaURL. This half
// is what the manager applies at start-up, and it has to keep passing the
// placeholder both shipped manifests carry
// (http://ci-placeholder.invalid:30581), or `kubectl apply -k
// config/default` CrashLoops the manager on a value the runbook says to
// replace later. That distinction is the whole reason there are two
// functions.
//
// Both live here, beside the code that builds URLs from this value, so the
// manager and the FrameInstall controller check the same thing rather than
// two copies of it. Callers wrap the error with the name of whatever they
// call this setting.
func ValidateMediaURLSyntax(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("is not set: every built image bakes this address into its own boot arguments, so an image built against it would fetch its autoinstall seed from nowhere")
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

// ValidateMediaURL is ValidateMediaURLSyntax plus the one thing syntax
// cannot see: whether this address could ever resolve.
//
// ".invalid" is reserved by RFC 2606 precisely so that it never does, and
// both shipped manifests default this setting to
// "http://ci-placeholder.invalid:30581" -- so the refusal added for a
// missing media URL never fired on the documented `kubectl apply -k`
// path, which is the path most likely to reach a real machine with a
// placeholder still in it. A BMC handed that address fails to resolve it
// and Frame sees a MediaAttached timeout that says nothing.
//
// This is what a build must pass, checked before the BMC is touched. It is
// not what the manager checks at start-up, on purpose: the placeholder has
// to keep the manager running so it can refuse the install and say why.
func ValidateMediaURL(raw string) error {
	if err := ValidateMediaURLSyntax(raw); err != nil {
		return err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q: %w", raw, err)
	}
	host := strings.ToLower(u.Hostname())
	if host == "invalid" || strings.HasSuffix(host, ".invalid") {
		return fmt.Errorf(
			"%q: .invalid is reserved by RFC 2606 and never resolves -- this is the placeholder the shipped manifests carry, not an address any BMC can fetch from", raw)
	}
	return nil
}

// LocalImageStore is the ImageStore `frame bootstrap` uses: it calls
// FetchBase and Remaster directly, because when no cluster exists there is
// nothing to host a build API.
type LocalImageStore struct {
	Dir      string
	Base     BaseSource
	MediaURL string // how the BMC reaches this machine, e.g. http://192.0.2.50:8081
}

func (s *LocalImageStore) Build(ctx context.Context, spec Spec) (imageURL, token string, err error) {
	// Same order as BuildHandler, for the same reason: refuse before
	// fetching or building anything.
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

	// No beacon base: nothing in the cold-start path reads a beacon.
	if err := writeSeed(s.Dir, tok, spec, ""); err != nil {
		// No token is returned for a half-written seed either.
		discardBuild(s.Dir, tok)
		return "", "", err
	}

	out := filepath.Join(s.Dir, tok+".iso")
	if err := Remaster(ctx, baseISO, spec, seedURL(s.MediaURL, tok), out); err != nil {
		// No token is returned, so no Remove will ever come for it.
		discardBuild(s.Dir, tok)
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
	return removeSeed(s.Dir, token)
}
