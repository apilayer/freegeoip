// Copyright 2009 The freegeoip authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package apiserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/apilayer/freegeoip"
)

func newTestHandler() (http.Handler, error) {
	_, f, _, _ := runtime.Caller(0)
	c := NewConfig()
	c.APIPrefix = "/api"
	c.PublicDir = "."
	c.DB = filepath.Join(filepath.Dir(f), "../testdata/db.gz")
	c.RateLimitLimit = 5
	c.RateLimitBackend = "map"
	c.Silent = true
	return NewHandler(c)
}

func TestHandler(t *testing.T) {
	f, err := newTestHandler()
	if err != nil {
		t.Fatal(err)
	}
	w := &httptest.ResponseRecorder{Body: &bytes.Buffer{}}
	r := &http.Request{
		Method:     "GET",
		URL:        &url.URL{Path: "/api/json/200.1.2.3"},
		RemoteAddr: "[::1]:1905",
	}
	f.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("Unexpected response: %d %s", w.Code, w.Body.String())
	}
	m := struct {
		Country string `json:"country_name"`
		City    string `json:"city"`
	}{}
	if err = json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m.Country != "Venezuela" && m.City != "Caracas" {
		t.Fatalf("Query data does not match: want Caracas,Venezuela, have %q,%q",
			m.City, m.Country)
	}
}

func TestMetricsHandler(t *testing.T) {
	f, err := newTestHandler()
	if err != nil {
		t.Fatal(err)
	}
	tp := []http.Request{
		{
			Method:     "GET",
			URL:        &url.URL{Path: "/api/json/200.1.2.3"},
			RemoteAddr: "[::1]:1905",
		},
		{
			Method:     "GET",
			URL:        &url.URL{Path: "/api/json/200.1.2.3"},
			RemoteAddr: "127.0.0.1:1905",
		},
		{
			Method:     "GET",
			URL:        &url.URL{Path: "/api/json/200.1.2.3"},
			RemoteAddr: "200.1.2.3:1905",
		},
	}
	for i, r := range tp {
		w := &httptest.ResponseRecorder{Body: &bytes.Buffer{}}
		f.ServeHTTP(w, &r)
		if w.Code != http.StatusOK {
			t.Fatalf("Test %d: Unexpected response: %d %s", i, w.Code, w.Body.String())
		}
	}
}

func TestWriters(t *testing.T) {
	f, err := newTestHandler()
	if err != nil {
		t.Fatal(err)
	}
	tp := []http.Request{
		{
			Method:     "GET",
			URL:        &url.URL{Path: "/api/csv/"},
			RemoteAddr: "[::1]:1905",
		},
		{
			Method:     "GET",
			URL:        &url.URL{Path: "/api/xml/"},
			RemoteAddr: "[::1]:1905",
		},
		{
			Method:     "GET",
			URL:        &url.URL{Path: "/api/json/"},
			RemoteAddr: "[::1]:1905",
		},
	}
	for i, r := range tp {
		w := &httptest.ResponseRecorder{Body: &bytes.Buffer{}}
		f.ServeHTTP(w, &r)
		if w.Code != http.StatusOK {
			t.Fatalf("Test %d: Unexpected response: %d %s", i, w.Code, w.Body.String())
		}
	}
}

func TestParseAcceptLanguage(t *testing.T) {
	var names = make(map[string]string)
	names["en"] = "Romania"
	names["de"] = "Rumänien"
	names["ro"] = "România"
	names["fr"] = "Roumanie"
	testParseAcceptLanguage(t, names, "de", "de")
	testParseAcceptLanguage(t, names, "de-DE", "de")
	testParseAcceptLanguage(t, names, "de-DE, en", "de")
	testParseAcceptLanguage(t, names, "en-US, de-DE", "en")
	testParseAcceptLanguage(t, names, "fr-CH, fr;q=0.9, en;q=0.8, de;q=0.7, *;q=0.5", "fr")
	testParseAcceptLanguage(t, names, "en;q=0.1, de;q=0.8, fr;q=0.7, *;q=0.5", "de")

	// less languages
	names = make(map[string]string)
	names["en"] = "Romania"
	names["de"] = "Rumänien"
	testParseAcceptLanguage(t, names, "fr-CH, fr;q=0.9, en;q=0.8, de;q=0.7, *;q=0.5", "en")

	// no languages
	names = make(map[string]string)
	testParseAcceptLanguage(t, names, "fr-CH, fr;q=0.9, en;q=0.8, de;q=0.7, *;q=0.5", "en")
}

func testParseAcceptLanguage(t *testing.T, names map[string]string, header string, language string) {
	result := parseAcceptLanguage(header, names)

	if result != language {
		t.Fatalf("Parsed language '%s' from header '%s'  doesn't match language '%s'", result, header, language)
	}
}

func TestGetErrorValues(t *testing.T) {
	testGetErrorValues(t, "error nil", &Config{FailOnIPNotFound: false}, nil, false, 0, "")
	testGetErrorValues(t, "generic error", &Config{FailOnIPNotFound: false}, errors.New("generic error"), true, 503, "Try again later.")
	testGetErrorValues(t, "unavailable error", &Config{FailOnIPNotFound: false}, freegeoip.ErrUnavailable, true, 503, "Try again later.")
	testGetErrorValues(t, "not found error", &Config{FailOnIPNotFound: false}, freegeoip.ErrNotFound, false, 0, "") //retrocompatibility

	testGetErrorValues(t, "error nil", &Config{FailOnIPNotFound: true}, nil, false, 0, "")
	testGetErrorValues(t, "generic error", &Config{FailOnIPNotFound: true}, errors.New("generic error"), true, 503, "Try again later.")
	testGetErrorValues(t, "unavailable error", &Config{FailOnIPNotFound: true}, freegeoip.ErrUnavailable, true, 503, "Try again later.")
	testGetErrorValues(t, "not found error", &Config{FailOnIPNotFound: true}, freegeoip.ErrNotFound, true, 404, "IP not found.")
}

func testGetErrorValues(t *testing.T, test string, conf *Config, err error, isErr bool, code int, msg string) {
	rIsErr, rCode, rMsg := getErrorValues(conf, err)

	if isErr != rIsErr {
		t.Fatalf("%s: Is error '%t' doesn't match '%t'", test, isErr, rIsErr)
	}
	if code != rCode {
		t.Fatalf("%s: Code '%d' doesn't match '%d'", test, code, rCode)
	}
	if msg != rMsg {
		t.Fatalf("%s: Message '%s' doesn't match '%s'", test, msg, rMsg)
	}
}
