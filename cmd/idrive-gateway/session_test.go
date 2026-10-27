package main

import (
	"idrive-unlimited/internal/evs"
	"idrive-unlimited/internal/session"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
)

func TestRefreshedSessionPersistsOnShutdown(t *testing.T) {
	jar, _ := session.NewJar()
	u, _ := url.Parse("https://evs.idrive.com/")
	jar.SetCookies(u, []*http.Cookie{{Name: "SESSION", Value: "before"}})
	client, _ := evs.NewClient(u.Host, jar)
	data, _ := client.SessionData()
	store, _ := session.NewStore(filepath.Join(t.TempDir(), "session.json"))
	if err := store.Save(data); err != nil {
		t.Fatal(err)
	}
	finish := startSessionPersistence(store, client)
	jar.SetCookies(u, []*http.Cookie{{Name: "SESSION", Value: "after"}})
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.Load()
	restored, _ := saved.NewCookieJar()
	cookies := restored.Cookies(u)
	if len(cookies) != 1 || cookies[0].Value != "after" || saved.ID != data.ID {
		t.Fatal("refreshed cookies or session identity lost")
	}
}
