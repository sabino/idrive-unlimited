package session

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCookieRoundTripPreservesIdentityAndAttributes(t *testing.T) {
	jar, _ := NewJar()
	u, _ := url.Parse("https://evs.idrive.com/evs/login")
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	jar.SetCookies(u, []*http.Cookie{{Name: "SESSION", Value: "root", Path: "/", Secure: true, HttpOnly: true, Expires: expires}, {Name: "SESSION", Value: "evs", Path: "/evs/", Domain: "idrive.com", Secure: true}})
	data, err := FromCookieJar("evs.idrive.com", jar)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Cookies) != 2 {
		t.Fatalf("cookies=%d", len(data.Cookies))
	}
	store, _ := NewStore(filepath.Join(t.TempDir(), "session.json"))
	if err := store.Save(data); err != nil {
		t.Fatal(err)
	}
	data, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := data.NewCookieJar()
	if err != nil {
		t.Fatal(err)
	}
	cookies := restored.Cookies(u)
	if len(cookies) != 2 || cookies[0].Value != "evs" || cookies[1].Value != "root" {
		t.Fatalf("lost path identity: %v", cookies)
	}
	for _, c := range data.Cookies {
		if c.Value == "root" && (!c.Secure || !c.HTTPOnly || !c.Expires.Equal(expires) || c.Domain != "") {
			t.Fatalf("lost cookie attributes: %+v", c)
		}
	}
	sibling, _ := url.Parse("https://other.idrive.com/evs/")
	if got := restored.Cookies(sibling); len(got) != 1 || got[0].Value != "evs" {
		t.Fatalf("lost host-only scope: %v", got)
	}
	jar.SetCookies(u, []*http.Cookie{{Name: "SESSION", Path: "/evs/", Domain: "idrive.com", MaxAge: -1}})
	data, _ = FromCookieJar("evs.idrive.com", jar)
	if len(data.Cookies) != 1 {
		t.Fatal("deleted cookie persisted")
	}
}

func TestSaveReplacesExistingFilePrivately(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session.json")
	os.WriteFile(p, []byte("old"), 0644)
	store, _ := NewStore(p)
	if err := store.Save(&Data{ServerHost: "example.com"}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(p)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
}

func TestSaveReplacesSymlinkWithoutChangingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte("untouched"), 0600)
	link := filepath.Join(dir, "session.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store, _ := NewStore(link)
	if err := store.Save(&Data{ServerHost: "example.com"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "untouched" {
		t.Fatal("followed session symlink")
	}
	if info, _ := os.Lstat(link); info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("session file still a symlink")
	}
}

func TestCookieExportRejectsLossyJar(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	if _, err := FromCookieJar("example.com", jar); err == nil {
		t.Fatal("silently exported lossy standard jar")
	}
}

func TestExpiredAndPublicSuffixCookiesNotPersisted(t *testing.T) {
	jar, _ := NewJar()
	u, _ := url.Parse("https://evs.idrive.com/evs/login")
	jar.SetCookies(u, []*http.Cookie{{Name: "expired", Value: "x", Expires: time.Now().Add(-time.Hour)}, {Name: "invalid", Value: "x", Domain: "com"}, {Name: "max-age", Value: "x", MaxAge: 60}})
	data, _ := FromCookieJar("evs.idrive.com", jar)
	if len(data.Cookies) != 1 || data.Cookies[0].Expires.IsZero() || data.Cookies[0].Path != "/evs" {
		t.Fatalf("unexpected cookies: %+v", data.Cookies)
	}
}
