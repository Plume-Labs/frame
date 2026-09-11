package main

import "testing"

// These mirror cmd/provisiond/main_test.go's own validateMediaURL cases:
// validateProvisiondMediaURL is the identical check, applied to a different
// flag, so a value that satisfies or defeats one satisfies or defeats the
// other for the same reasons -- see that file's comments for why each
// malformed shape below is not caught by a presence-only check.

func TestValidateProvisiondMediaURLAcceptsAWellFormedHTTPURL(t *testing.T) {
	if err := validateProvisiondMediaURL("http://192.168.2.50:30581"); err != nil {
		t.Errorf("want no error, got %v", err)
	}
}

func TestValidateProvisiondMediaURLAcceptsAWellFormedHTTPSURL(t *testing.T) {
	if err := validateProvisiondMediaURL("https://media.frame.internal"); err != nil {
		t.Errorf("want no error, got %v", err)
	}
}

// The check this function exists for: presence alone was the entire guard
// before (an unset -provisiond-media-url used to be handed straight to
// HTTPImageStore, which would build a relative "/iso/<token>.iso" URL and
// hand it to a BMC that cannot resolve it), and presence alone never caught
// this.
func TestValidateProvisiondMediaURLRefusesAnEmptyValue(t *testing.T) {
	if err := validateProvisiondMediaURL(""); err == nil {
		t.Fatal("want an error for an empty value, got nil")
	}
}

func TestValidateProvisiondMediaURLRefusesAWrongScheme(t *testing.T) {
	if err := validateProvisiondMediaURL("ftp://192.168.2.50:30581"); err == nil {
		t.Fatal("want an error for a non-http(s) scheme, got nil")
	}
}

// url.Parse accepts this without error -- "http:///preseed" has scheme
// "http" and an empty Host, everything after the third slash lands in Path.
func TestValidateProvisiondMediaURLRefusesAnHTTPURLWithNoHost(t *testing.T) {
	if err := validateProvisiondMediaURL("http:///preseed"); err == nil {
		t.Fatal("want an error for a URL with no host, got nil")
	}
}

// url.Parse accepts a bare hostname with no scheme too -- the whole string
// lands in Path, and Scheme and Host both come back empty. A presence-only
// check (raw == "") could never catch this, because the value is not empty.
func TestValidateProvisiondMediaURLRefusesASchemelessValue(t *testing.T) {
	if err := validateProvisiondMediaURL("media.frame.internal:30581"); err == nil {
		t.Fatal("want an error for a value with no scheme, got nil")
	}
}

// A value url.Parse itself rejects (not just one that parses to something
// unusable) must still surface as a clear error, not a panic.
func TestValidateProvisiondMediaURLRefusesAValueURLParseItselfRejects(t *testing.T) {
	if err := validateProvisiondMediaURL("http://192.168.2.50:30581/%zz"); err == nil {
		t.Fatal("want an error for a value url.Parse itself rejects, got nil")
	}
}
