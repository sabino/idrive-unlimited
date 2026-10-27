package evs

import (
	"context"
	"encoding/json"
	"errors"
	"idrive-unlimited/internal/session"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveNamespaceRename(t *testing.T) {
	if os.Getenv("IDRIVE_LIVE_SESSION") == "" {
		t.Skip("set IDRIVE_LIVE_SESSION for explicit live scratch testing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	raw, err := os.ReadFile(os.Getenv("IDRIVE_LIVE_SESSION"))
	if err != nil {
		t.Fatal(err)
	}
	var data session.Data
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	c, err := NewClientFromSession(&data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		updated, err := c.SessionData()
		if err == nil {
			store, _ := session.NewStore(os.Getenv("IDRIVE_LIVE_SESSION"))
			err = store.SaveLoaded(updated, &data)
		}
		if err != nil {
			t.Logf("live session persistence: %v", err)
		}
	}()
	id, _ := randomUploadID()
	root := "/.idrive-native-rename-" + id
	if err = c.Mkdir(ctx, root); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.Delete(cleanup, root); err != nil {
			t.Errorf("scratch cleanup failed: %v", err)
		}
	}()
	for _, dir := range []string{"a", "b", "a/tree", "a/tree/child"} {
		if err = c.Mkdir(ctx, root+"/"+dir); err != nil {
			t.Fatal(err)
		}
	}
	local := filepath.Join(t.TempDir(), "payload")
	os.WriteFile(local, []byte("synthetic namespace test"), 0600)
	for _, name := range []string{"a/source.txt", "b/existing.txt", "a/tree/child/file.txt", "a/back\\slash.txt"} {
		if err = c.UploadFile(ctx, local, root+"/"+name, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	direct := func(old, new string) error {
		v := url.Values{"oldpath": {root + "/" + old}, "newpath": {root + "/" + new}, "json": {"yes"}}
		var reply apiReply
		if err := c.postForm(ctx, "/evs/renameFileFolder", v, &reply); err != nil {
			return err
		}
		if !isSuccess(reply.Message) {
			return apiError(200, reply.Message, reply.Desc)
		}
		c.invalidatePaths(root)
		return nil
	}
	for _, pair := range [][2]string{{"a/source.txt", "b/different.txt"}, {"a/tree", "b/different-tree"}, {"a/back\\slash.txt", "b/new\\name.txt"}} {
		err := direct(pair[0], pair[1])
		t.Logf("native cross-directory rename (%s): %v", pair[0], err)
		if err == nil {
			if _, err = c.StatFresh(ctx, root+"/"+pair[1]); err != nil {
				t.Fatal("successful reply without expected destination", err)
			}
			if _, err = c.StatFresh(ctx, root+"/"+pair[0]); !errors.Is(err, ErrNotFound) {
				t.Fatal("source still present", err)
			}
		}
	}
	rc, err := c.Download(ctx, root+"/b/different-tree/child/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(content) != "synthetic namespace test" {
		t.Fatal("nested bytes did not survive directory rename", err)
	}
	err = direct("a/missing.txt", "b/existing.txt")
	t.Logf("missing-source existing-target reply: %v", err)
	if _, statErr := c.StatFresh(ctx, root+"/b/existing.txt"); statErr != nil {
		t.Fatal("native failed rename removed the existing destination", statErr)
	}
}
