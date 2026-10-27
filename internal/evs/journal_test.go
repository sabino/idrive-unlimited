package evs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"idrive-unlimited/internal/session"
	"idrive-unlimited/internal/testevs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func recordForTest() uploadRecord {
	id := "12345678-1234-1234-1234-123456789abc"
	return uploadRecord{Version: 1, ID: id, Dest: "/dest.txt", Stage: "/.idrive-gateway-upload-" + id + ".txt", Backup: "/.idrive-gateway-backup-" + id + ".txt", Size: 3, Hash: digest("new"), HadOriginal: true, OriginalSize: 3, OriginalHash: digest("old"), Ready: true}
}
func TestRecoveryAcrossInterruptedReplacementPhases(t *testing.T) {
	for _, phase := range []string{"staging", "prepared", "backed-up", "promoted", "committed-later-edit", "external-conflict"} {
		t.Run(phase, func(t *testing.T) {
			s := testevs.New(t)
			c, _ := NewClient(s.Host, nil)
			root := t.TempDir()
			if err := c.EnableRecovery(context.Background(), root); err != nil {
				t.Fatal(err)
			}
			r := recordForTest()
			s.Add(r.Stage, []byte("new"))
			want := "old"
			switch phase {
			case "staging":
				r.Ready = false
				s.Add(r.Dest, []byte("old"))
			case "prepared":
				s.Add(r.Dest, []byte("old"))
			case "backed-up":
				s.Add(r.Backup, []byte("old"))
			case "promoted":
				s.Add(r.Backup, []byte("old"))
				s.Add(r.Dest, []byte("new"))
				want = "new"
			case "committed-later-edit":
				r.Committed = true
				s.Add(r.Backup, []byte("old"))
				s.Add(r.Dest, []byte("later edit"))
				want = "later edit"
			case "external-conflict":
				s.Add(r.Backup, []byte("old"))
				s.Add(r.Dest, []byte("unrelated"))
				want = "unrelated"
			}
			if err := c.saveUpload(&r); err != nil {
				t.Fatal(err)
			}
			data, _ := c.SessionData()
			c.CloseRecovery()
			restarted, _ := NewClientFromSession(data)
			err := restarted.EnableRecovery(context.Background(), root)
			if (err != nil) != (phase == "external-conflict") {
				t.Fatalf("recovery error=%v", err)
			}
			defer restarted.CloseRecovery()
			if content, _ := s.Read(r.Dest); string(content) != want {
				t.Fatalf("destination=%q want=%q", content, want)
			}
			if phase == "external-conflict" {
				for _, name := range []string{r.Stage, r.Backup} {
					if _, ok := s.Read(name); !ok {
						t.Fatal("conflict discarded recovery bytes")
					}
				}
			} else {
				if len(s.Paths()) != 1 {
					t.Fatalf("unreconciled paths: %v", s.Paths())
				}
				files, _ := filepath.Glob(filepath.Join(root, c.CacheIdentity(), "*.json"))
				if len(files) != 0 {
					t.Fatal("journal not completed")
				}
			}
		})
	}
}
func TestRecoveryLockAndSessionNamespace(t *testing.T) {
	s := testevs.New(t)
	c, _ := NewClient(s.Host, nil)
	data, _ := c.SessionData()
	root := t.TempDir()
	if err := c.EnableRecovery(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	defer c.CloseRecovery()
	other, _ := NewClientFromSession(data)
	if err := other.EnableRecovery(context.Background(), root); err == nil {
		other.CloseRecovery()
		t.Fatal("two servers acquired the same journal")
	}
	different, _ := NewClient(s.Host, nil)
	if err := different.EnableRecovery(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	different.CloseRecovery()
	r := recordForTest()
	r.Stage = "/unrelated"
	if err := validateUploadRecord(r, r.ID+".json"); err == nil {
		t.Fatal("accepted unrelated recovery path")
	}
}

func TestMoveRecoveryAcrossInterruptedPhases(t *testing.T) {
	for _, phase := range []string{"prepared-identical", "backed-up", "move-started", "promoted", "committed-recreated-source"} {
		t.Run(phase, func(t *testing.T) {
			s := testevs.New(t)
			c, _ := NewClient(s.Host, nil)
			root := t.TempDir()
			if err := c.EnableRecovery(context.Background(), root); err != nil {
				t.Fatal(err)
			}
			r := recordForTest()
			r.Source, r.Stage = "/source.txt", "/source.txt"
			wantDest, wantSource := "old", "new"
			switch phase {
			case "prepared-identical":
				r.OriginalHash = r.Hash
				s.Add(r.Dest, []byte("new"))
				wantDest = "new"
			case "backed-up", "move-started":
				s.Add(r.Backup, []byte("old"))
				r.MoveStarted = phase == "move-started"
			case "promoted":
				r.MoveStarted = true
				s.Add(r.Backup, []byte("old"))
				s.Add(r.Dest, []byte("new"))
				wantDest, wantSource = "new", ""
			case "committed-recreated-source":
				r.MoveStarted, r.Committed = true, true
				s.Add(r.Backup, []byte("old"))
				s.Add(r.Dest, []byte("later destination"))
				wantDest, wantSource = "later destination", "new external source"
			}
			if wantSource != "" {
				s.Add(r.Source, []byte(wantSource))
			}
			if err := c.saveUpload(&r); err != nil {
				t.Fatal(err)
			}
			data, _ := c.SessionData()
			c.CloseRecovery()
			restarted, _ := NewClientFromSession(data)
			if err := restarted.EnableRecovery(context.Background(), root); err != nil {
				t.Fatal(err)
			}
			defer restarted.CloseRecovery()
			if got, _ := s.Read(r.Dest); string(got) != wantDest {
				t.Fatalf("destination=%q want=%q", got, wantDest)
			}
			if got, exists := s.Read(r.Source); string(got) != wantSource || exists != (wantSource != "") {
				t.Fatalf("source=%q exists=%v want=%q", got, exists, wantSource)
			}
			if _, exists := s.Read(r.Backup); exists {
				t.Fatal("recovery backup was not reconciled")
			}
			files, _ := filepath.Glob(filepath.Join(root, c.CacheIdentity(), "*.json"))
			if len(files) != 0 {
				t.Fatal("completed move journal remains")
			}
		})
	}
}
func TestJournalReconcilesLostPromotionReply(t *testing.T) {
	s := testevs.New(t)
	s.Add("/dest.txt", []byte("old"))
	s.Configure(testevs.Options{LostPromotionReply: true})
	c, _ := NewClient(s.Host, nil)
	if err := c.EnableRecovery(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer c.CloseRecovery()
	local := filepath.Join(t.TempDir(), "payload")
	os.WriteFile(local, []byte("new"), 0600)
	if err := c.UploadFile(context.Background(), local, "/dest.txt", time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(s.Paths()) != 1 {
		t.Fatal(s.Paths())
	}
}
func TestDeviceRootCannotBeDeletedOrMoved(t *testing.T) {
	s := testevs.New(t)
	s.Device("/raw", "Friendly")
	c, _ := NewClient(s.Host, nil)
	for _, op := range []func() error{func() error { return c.Delete(context.Background(), "/raw") }, func() error { return c.Rename(context.Background(), "/raw", "/new") }, func() error { return c.Move(context.Background(), "/raw", "/nested") }} {
		if err := op(); !errors.Is(err, ErrReadOnly) {
			t.Fatalf("device mutation allowed: %v", err)
		}
	}
	if len(s.Deletes()) != 0 {
		t.Fatal("device root deleted")
	}
}

// Kill an actual uploader after the server renames the original, while its
// response is withheld. A new client must reconcile the on-disk journal.
func TestRecoveryAfterUploaderProcessIsKilled(t *testing.T) {
	if os.Getenv("IDRIVE_FIXTURE_CHILD") == "1" {
		der, err := base64.StdEncoding.DecodeString(os.Getenv("IDRIVE_FIXTURE_CA"))
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		pool.AddCert(cert)
		http.DefaultTransport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
		raw, err := os.ReadFile(os.Getenv("IDRIVE_FIXTURE_SESSION"))
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
		if err = c.EnableRecovery(context.Background(), os.Getenv("IDRIVE_FIXTURE_JOURNAL")); err != nil {
			t.Fatal(err)
		}
		defer c.CloseRecovery()
		if err = c.UploadFile(context.Background(), os.Getenv("IDRIVE_FIXTURE_PAYLOAD"), "/dest.txt", time.Now()); err != nil {
			t.Fatal(err)
		}
		t.Fatal("upload should have been interrupted")
	}
	s := testevs.New(t)
	s.Add("/dest.txt", []byte("old"))
	reached, hold := make(chan struct{}, 1), make(chan struct{})
	defer func() {
		select {
		case <-hold:
		default:
			close(hold)
		}
	}()
	s.Configure(testevs.Options{BackupRenamed: reached, HoldBackupReply: hold})
	c, _ := NewClient(s.Host, nil)
	data, _ := c.SessionData()
	root := t.TempDir()
	saved, payload, journal := filepath.Join(root, "session.json"), filepath.Join(root, "payload"), filepath.Join(root, "journal")
	raw, _ := json.Marshal(data)
	os.WriteFile(saved, raw, 0600)
	os.WriteFile(payload, []byte("new"), 0600)
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryAfterUploaderProcessIsKilled$")
	cmd.Env = append(os.Environ(), "IDRIVE_FIXTURE_CHILD=1", "IDRIVE_FIXTURE_CA="+base64.StdEncoding.EncodeToString(s.CertificateDER()), "IDRIVE_FIXTURE_SESSION="+saved, "IDRIVE_FIXTURE_JOURNAL="+journal, "IDRIVE_FIXTURE_PAYLOAD="+payload)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reached:
	case <-time.After(15 * time.Second):
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("child did not reach backup boundary: %s", output.String())
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	close(hold)
	restarted, _ := NewClientFromSession(data)
	if err := restarted.EnableRecovery(context.Background(), journal); err != nil {
		t.Fatal(err)
	}
	defer restarted.CloseRecovery()
	if got, _ := s.Read("/dest.txt"); string(got) != "old" {
		t.Fatalf("original not restored after process crash: %q", got)
	}
	if len(s.Paths()) != 1 {
		t.Fatal("temporary paths remain", s.Paths())
	}
}
