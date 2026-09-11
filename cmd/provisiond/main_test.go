package main

import "testing"

func TestValidateMediaURLAcceptsAWellFormedHTTPURL(t *testing.T) {
	if err := validateMediaURL("http://192.168.2.50:8081"); err != nil {
		t.Errorf("want no error, got %v", err)
	}
}

func TestValidateMediaURLAcceptsAWellFormedHTTPSURL(t *testing.T) {
	if err := validateMediaURL("https://media.frame.internal"); err != nil {
		t.Errorf("want no error, got %v", err)
	}
}

// The check this whole function exists for: presence alone was the entire
// guard before, and it never caught this.
func TestValidateMediaURLRefusesAnEmptyValue(t *testing.T) {
	err := validateMediaURL("")
	if err == nil {
		t.Fatal("want an error for an empty MEDIA_URL, got nil")
	}
}

func TestValidateMediaURLRefusesAWrongScheme(t *testing.T) {
	err := validateMediaURL("ftp://192.168.2.50:8081")
	if err == nil {
		t.Fatal("want an error for a non-http(s) scheme, got nil")
	}
}

// url.Parse accepts this without error -- "http:///preseed" has scheme
// "http" and an empty Host, everything after the third slash lands in
// Path. A value shaped like this would build images whose boot arguments
// carry a URL with no address to fetch from at all.
func TestValidateMediaURLRefusesAnHTTPURLWithNoHost(t *testing.T) {
	err := validateMediaURL("http:///preseed")
	if err == nil {
		t.Fatal("want an error for a URL with no host, got nil")
	}
}

// url.Parse accepts a bare hostname with no scheme without error too --
// the whole string lands in Path, and Scheme and Host both come back
// empty. This is the case a presence-only check (cfg.MediaURL == "") could
// never catch, because the value is not empty.
func TestValidateMediaURLRefusesASchemelessValue(t *testing.T) {
	err := validateMediaURL("media.frame.internal:8081")
	if err == nil {
		t.Fatal("want an error for a value with no scheme, got nil")
	}
}

// A value url.Parse itself rejects (not just one that parses to something
// unusable) must still surface as a config error, not a panic or a bare
// url.Parse error with no MEDIA_URL context.
func TestValidateMediaURLRefusesAValueURLParseItselfRejects(t *testing.T) {
	err := validateMediaURL("http://192.168.2.50:8081/%zz")
	if err == nil {
		t.Fatal("want an error for a value url.Parse itself rejects, got nil")
	}
}
