package evs

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"idrive-unlimited/internal/testevs"
)

func TestFailedReplacementsPreserveOriginal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		options  testevs.Options
		restored bool
	}{
		{"upload", testevs.Options{FailUpload: true}, true},
		{"checksum", testevs.Options{CorruptStage: true}, true},
		{"promotion rollback", testevs.Options{FailPromotion: true}, true},
		{"backup rename", testevs.Options{FailBackup: true}, true},
		{"lost promotion reply", testevs.Options{LostPromotionReply: true}, false},
		{"lost backup reply", testevs.Options{LostBackupReply: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testevs.New(t)
			s.Add("/old.txt", []byte("original"))
			s.Configure(tc.options)
			c, _ := NewClient(s.Host, nil)
			local := filepath.Join(t.TempDir(), "new")
			os.WriteFile(local, []byte("replacement"), 0600)
			if err := c.UploadFile(context.Background(), local, "/old.txt", time.Now()); err == nil {
				t.Fatal("expected injected failure")
			}
			found := false
			for _, p := range s.Paths() {
				data, _ := s.Read(p)
				if string(data) == "original" {
					found = true
				}
			}
			if !found {
				t.Fatal("original bytes were lost")
			}
			if tc.restored {
				if data, _ := s.Read("/old.txt"); string(data) != "original" {
					t.Fatalf("original not preserved/restored: %q", data)
				}
			}
			for _, p := range s.Deletes() {
				if p == "/old.txt" {
					t.Fatal("deleted original destination")
				}
			}
		})
	}
}

func TestVerifiedUploads(t *testing.T) {
	for _, size := range []int{0, 123, 2*1024*1024 + 123} {
		t.Run(stringSize(size), func(t *testing.T) {
			s := testevs.New(t)
			s.Add("/old.txt", []byte("original"))
			c, _ := NewClient(s.Host, nil)
			data := bytes.Repeat([]byte("x"), size)
			local := filepath.Join(t.TempDir(), "payload")
			os.WriteFile(local, data, 0600)
			for _, dest := range []string{"/new.txt", "/old.txt"} {
				if err := c.UploadFile(context.Background(), local, dest, time.Now()); err != nil {
					t.Fatal(err)
				}
				got, ok := s.Read(dest)
				if !ok || !bytes.Equal(got, data) {
					t.Fatal("upload content mismatch")
				}
			}
			for _, p := range s.Paths() {
				if strings.Contains(p, ".idrive-gateway-") {
					t.Fatalf("temporary remote file left behind: %s", p)
				}
			}
		})
	}
}
func stringSize(n int) string {
	if n == 0 {
		return "empty"
	}
	if n < 1024 {
		return "small"
	}
	return "chunked"
}

func TestUploadLimitBeforeRemoteMutation(t *testing.T) {
	s := testevs.New(t)
	c, _ := NewClient(s.Host, nil)
	p := filepath.Join(t.TempDir(), "large")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxUploadSize + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if err := c.UploadFile(context.Background(), p, "/old.txt", time.Now()); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("got %v", err)
	}
	if s.Requests() != 0 {
		t.Fatal("oversized upload contacted server")
	}
}

func TestCopyAndMoveRefreshCachedListings(t *testing.T) {
	s := testevs.New(t)
	s.Add("/source.txt", []byte("abc"))
	s.Directory("/copy")
	s.Directory("/move")
	c, _ := NewClient(s.Host, nil)
	c.Browse(context.Background(), "/")
	c.Browse(context.Background(), "/copy")
	c.Browse(context.Background(), "/move")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Copy(ctx, "/source.txt", "/copy"); err != nil {
		t.Fatal(err)
	}
	if err := c.Move(ctx, "/source.txt", "/move"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Read("/source.txt"); ok {
		t.Fatal("move left source")
	}
}

func TestMutationResponseValidation(t *testing.T) {
	for _, body := range []string{"", `<html><form>login</form></html>`, `{"message":`, `{}`, `{"other":"ok"}`} {
		if err := checkMaybeJSONReply([]byte(body)); err == nil {
			t.Errorf("accepted %q", body)
		}
	}
	if err := checkMaybeJSONReply([]byte(`{"message":"SUCCESS"}`)); err != nil {
		t.Fatal(err)
	}
	if err := checkMaybeJSONReply([]byte(`{"message":"INVALID_SESSION"}`)); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
}

func TestCopyAndMoveRejectExistingTargets(t *testing.T) {
	s := testevs.New(t)
	s.Add("/source.txt", []byte("source"))
	s.Add("/dest/source.txt", []byte("existing"))
	c, _ := NewClient(s.Host, nil)
	for _, operation := range []func(context.Context, string, string) error{c.Copy, c.Move} {
		if err := operation(context.Background(), "/source.txt", "/dest"); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("got %v", err)
		}
		if data, _ := s.Read("/dest/source.txt"); string(data) != "existing" {
			t.Fatal("replaced collision target")
		}
		if data, _ := s.Read("/source.txt"); string(data) != "source" {
			t.Fatal("altered source")
		}
	}
}
