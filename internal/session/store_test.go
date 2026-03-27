package session

import (
	"net/http/cookiejar"
	"testing"
)

func TestFromCookieJarIncludesPathScopedEVSCookies(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}

	data := &Data{
		ServerHost: "example.com",
		Cookies: []Cookie{
			{
				Name:     "EVSID",
				Value:    "abc",
				Path:     "/evs/",
				Domain:   "example.com",
				Secure:   true,
				HTTPOnly: true,
			},
			{
				Name:     "JSESSIONID",
				Value:    "def",
				Path:     "/evs",
				Domain:   "example.com",
				Secure:   true,
				HTTPOnly: true,
			},
		},
	}
	if err := data.ApplyToJar(jar); err != nil {
		t.Fatal(err)
	}

	roundTrip, err := FromCookieJar("example.com", jar)
	if err != nil {
		t.Fatal(err)
	}
	if len(roundTrip.Cookies) != 2 {
		t.Fatalf("expected 2 cookies after round-trip, got %d", len(roundTrip.Cookies))
	}
}

func TestApplyToJarSupportsPathScopedCookies(t *testing.T) {
	data := &Data{
		ServerHost: "example.com",
		Cookies: []Cookie{
			{
				Name:     "EVSID",
				Value:    "abc",
				Path:     "/evs/",
				Domain:   "example.com",
				Secure:   true,
				HTTPOnly: true,
			},
		},
	}

	jar, err := data.NewCookieJar()
	if err != nil {
		t.Fatal(err)
	}
	base, err := data.BaseURL()
	if err != nil {
		t.Fatal(err)
	}
	if len(jar.Cookies(base)) != 0 {
		t.Fatalf("expected no root cookies, got %d", len(jar.Cookies(base)))
	}
	evsURL := *base
	evsURL.Path = "/evs/"
	cookies := jar.Cookies(&evsURL)
	if len(cookies) != 1 {
		t.Fatalf("expected 1 /evs cookie, got %d", len(cookies))
	}
	if cookies[0].Name != "EVSID" {
		t.Fatalf("expected EVSID, got %s", cookies[0].Name)
	}
}

func TestFromCookieJarDeduplicatesAcrossPaths(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}

	data := &Data{
		ServerHost: "example.com",
		Cookies: []Cookie{
			{
				Name:   "A",
				Value:  "1",
				Path:   "/",
				Domain: "example.com",
			},
			{
				Name:   "B",
				Value:  "2",
				Path:   "/evs/",
				Domain: "example.com",
			},
		},
	}
	if err := data.ApplyToJar(jar); err != nil {
		t.Fatal(err)
	}

	got, err := FromCookieJar("example.com", jar)
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, item := range got.Cookies {
		seen[item.Name] = true
	}
	for _, want := range []string{"A", "B"} {
		if !seen[want] {
			t.Fatalf("missing cookie %s in exported session", want)
		}
	}
}
