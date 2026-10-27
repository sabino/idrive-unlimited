package idrivewebdav

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"idrive-unlimited/internal/evs"
	"idrive-unlimited/internal/testevs"
)

func TestNegotiatedMoveReplacement(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault testevs.Options
		want  int
	}{
		{"success", testevs.Options{}, 204},
		{"upload failure", testevs.Options{FailUpload: true}, 403},
		{"promotion failure", testevs.Options{FailPromotion: true}, 403},
		{"corrupt upload", testevs.Options{CorruptStage: true}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testevs.New(t)
			s.Add("/source.txt", []byte("replacement"))
			s.Add("/nested/dest.txt", []byte("original"))
			s.Configure(tc.fault)
			c, _ := evs.NewClient(s.Host, nil)
			h := NewHandler(New(c, "/"), nil)
			r := httptest.NewRequest("MOVE", "http://gateway/source.txt", nil)
			r.Header.Set("Destination", "http://gateway/nested/dest.txt")
			r.Header.Set("Overwrite", "T")
			r.Header.Set(SafeMoveHeader, SafeMoveVersion)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if (w.Code < 400) != (tc.want < 400) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			want := "original"
			if tc.want < 400 {
				want = "replacement"
			}
			if data, _ := s.Read("/nested/dest.txt"); string(data) != want {
				t.Fatalf("destination=%q want=%q", data, want)
			}
			data, exists := s.Read("/source.txt")
			if tc.want >= 400 && (!exists || string(data) != "replacement") {
				t.Fatal("failed move lost source")
			}
			if tc.want < 400 && exists {
				t.Fatal("successful move retained source")
			}
			for _, deleted := range s.Deletes() {
				if deleted == "/nested/dest.txt" {
					t.Fatal("destination was pre-deleted")
				}
			}
		})
	}
}

func TestMoveAmbiguousRepliesPreserveBothVersions(t *testing.T) {
	for _, fault := range []testevs.Options{{LostBackupReply: true}, {LostPromotionReply: true}} {
		s := testevs.New(t)
		s.Add("/source.txt", []byte("replacement"))
		s.Add("/dest.txt", []byte("original"))
		s.Configure(fault)
		c, _ := evs.NewClient(s.Host, nil)
		r := httptest.NewRequest("MOVE", "http://gateway/source.txt", nil)
		r.Header.Set("Destination", "http://gateway/dest.txt")
		r.Header.Set("Overwrite", "T")
		r.Header.Set(SafeMoveHeader, SafeMoveVersion)
		w := httptest.NewRecorder()
		NewHandler(New(c, "/"), nil).ServeHTTP(w, r)
		if w.Code < 400 {
			t.Fatal("ambiguous move returned success")
		}
		if data, _ := s.Read("/source.txt"); string(data) != "replacement" {
			t.Fatal("ambiguous move removed source")
		}
		found := false
		for _, p := range s.Paths() {
			data, _ := s.Read(p)
			if string(data) == "original" {
				found = true
			}
		}
		if !found {
			t.Fatal("ambiguous move lost original destination")
		}
	}
}

func TestCompanionMountReplacement(t *testing.T) {
	if os.Getenv("IDRIVE_RCLONE_FUSE_TEST") != "1" {
		t.Skip("requires /dev/fuse and fusermount3")
	}
	binary := os.Getenv("IDRIVE_RCLONE_TEST_BINARY")
	if binary == "" {
		t.Fatal("IDRIVE_RCLONE_TEST_BINARY is required")
	}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			s := testevs.New(t)
			s.Add("/source.txt", []byte("replacement"))
			s.Add("/dest.txt", []byte("original"))
			s.Configure(testevs.Options{FailUpload: fail})
			c, _ := evs.NewClient(s.Host, nil)
			server := httptest.NewServer(NewHandler(New(c, "/"), nil))
			defer server.Close()
			mount := t.TempDir()
			cmd := exec.Command(binary, "mount", ":webdav:", mount, "--webdav-url", server.URL, "--webdav-idrive-safe-move", "--vfs-cache-mode", "full", "--cache-dir", t.TempDir(), "--no-modtime", "--config", os.DevNull, "--retries", "1", "--low-level-retries", "1")
			log, err := os.CreateTemp(t.TempDir(), "mount-log")
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd.Stdout, cmd.Stderr = log, log
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { exec.Command("fusermount3", "-u", mount).Run(); cmd.Process.Kill(); cmd.Wait() }()
			ready := false
			for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
				if exec.Command("mountpoint", "-q", mount).Run() == nil {
					ready = true
					break
				}
			}
			if !ready {
				data, _ := os.ReadFile(log.Name())
				t.Fatalf("mount did not start: %s", data)
			}
			err = os.Rename(filepath.Join(mount, "source.txt"), filepath.Join(mount, "dest.txt"))
			if (err != nil) != fail {
				data, _ := os.ReadFile(log.Name())
				t.Fatalf("rename error=%v log=%s", err, data)
			}
			want := "replacement"
			if fail {
				want = "original"
			}
			if data, err := os.ReadFile(filepath.Join(mount, "dest.txt")); err != nil || string(data) != want {
				t.Fatalf("mounted destination=%q err=%v", data, err)
			}
			if fail {
				if data, err := os.ReadFile(filepath.Join(mount, "source.txt")); err != nil || string(data) != "replacement" {
					t.Fatalf("failed rename corrupted VFS source view: %q %v", data, err)
				}
			}
			if data, _ := s.Read("/dest.txt"); string(data) != want {
				t.Fatalf("remote destination=%q", data)
			}
		})
	}
}

// This exercises operations.Move in the actual companion executable, including
// its capability negotiation and its destination-deletion decision.
func TestCompanionMove(t *testing.T) {
	binary := os.Getenv("IDRIVE_RCLONE_TEST_BINARY")
	if binary == "" {
		t.Skip("set IDRIVE_RCLONE_TEST_BINARY to exercise the real companion")
	}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			s := testevs.New(t)
			s.Add("/source.txt", []byte("replacement"))
			s.Add("/dest.txt", []byte("original"))
			s.Configure(testevs.Options{FailUpload: fail})
			c, _ := evs.NewClient(s.Host, nil)
			h := NewHandler(New(c, "/"), nil)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// A replacement must never issue a separate DELETE.
				if r.Method == "DELETE" {
					t.Error("client sent a pre-delete")
				}
				h.ServeHTTP(w, r)
			}))
			defer server.Close()
			cmd := exec.Command(binary, "moveto", ":webdav:source.txt", ":webdav:dest.txt", "--webdav-url", server.URL, "--webdav-idrive-safe-move", "--retries", "1", "--low-level-retries", "1", "--config", os.DevNull)
			out, err := cmd.CombinedOutput()
			if (err != nil) != fail {
				t.Fatalf("move error=%v output=%s", err, out)
			}
			want := "replacement"
			if fail {
				want = "original"
			}
			if data, _ := s.Read("/dest.txt"); string(data) != want {
				t.Fatalf("destination=%q output=%s", data, out)
			}
		})
	}
	t.Run("refuses unadvertised server", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "OPTIONS" {
				t.Errorf("unsafe request: %s", r.Method)
			}
			w.WriteHeader(200)
		}))
		defer s.Close()
		cmd := exec.Command(binary, "lsf", ":webdav:", "--webdav-url", s.URL, "--webdav-idrive-safe-move", "--config", os.DevNull)
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "does not advertise") {
			t.Fatalf("err=%v output=%s", err, out)
		}
	})
}

func TestCompanionEncodingIsReversibleAndRejectsAmbiguity(t *testing.T) {
	binary := os.Getenv("IDRIVE_RCLONE_TEST_BINARY")
	if binary == "" {
		t.Skip("requires companion executable")
	}
	for _, raw := range []string{" spaced.txt␠", "unsafe.txt "} {
		t.Run(raw, func(t *testing.T) {
			s := testevs.New(t)
			s.Add("/"+raw, []byte("data"))
			c, _ := evs.NewClient(s.Host, nil)
			server := httptest.NewServer(NewHandler(New(c, "/"), nil))
			defer server.Close()
			cmd := exec.Command(binary, "lsf", ":webdav:", "--webdav-url", server.URL, "--webdav-encoding", "Slash,Dot,Ctl,Del,RightSpace,RightCrLfHtVt,InvalidUtf8", "--config", os.DevNull)
			out, err := cmd.CombinedOutput()
			if raw == "unsafe.txt " {
				if err == nil || !strings.Contains(string(out), "cannot round-trip") {
					t.Fatalf("ambiguous raw name was silently decoded: %v %s", err, out)
				}
			} else if err != nil || !strings.Contains(string(out), " spaced.txt \n") {
				t.Fatalf("encoded name did not round-trip: %v %s", err, out)
			}
		})
	}
}
