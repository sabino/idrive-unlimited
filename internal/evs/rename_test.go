package evs

import (
	"context"
	"idrive-unlimited/internal/testevs"
	"testing"
)

func TestCrossParentRenameAvoidsIntermediateBasenameCollision(t *testing.T) {
	s := testevs.New(t)
	s.Directory("/a")
	s.Directory("/b")
	s.Add("/a/source.txt", []byte("moving"))
	s.Add("/b/source.txt", []byte("keep"))
	s.Directory("/a/tree")
	s.Directory("/a/tree/child")
	s.Add("/a/tree/child/file", []byte("nested"))
	s.Directory("/b/tree")
	c, _ := NewClient(s.Host, nil)
	for _, pair := range [][2]string{{"/a/source.txt", "/b/different.txt"}, {"/a/tree", "/b/different-tree"}} {
		if err := c.Rename(context.Background(), pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	if data, _ := s.Read("/b/source.txt"); string(data) != "keep" {
		t.Fatal("unrelated destination basename changed")
	}
	if data, _ := s.Read("/b/different-tree/child/file"); string(data) != "nested" {
		t.Fatal("nested directory bytes did not move")
	}
	if _, err := c.StatFresh(context.Background(), "/b/different-tree/child"); err != nil {
		t.Fatal("nested directory did not move", err)
	}
}
func TestMoveRecoveryNeverDeletesRecreatedSource(t *testing.T) {
	s := testevs.New(t)
	s.Add("/source.txt", []byte("moving"))
	s.Add("/dest.txt", []byte("old"))
	s.Configure(testevs.Options{LostRenameSource: "/source.txt", RecreateSource: "/source.txt"})
	c, _ := NewClient(s.Host, nil)
	if err := c.EnableRecovery(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer c.CloseRecovery()
	if err := c.MoveReplace(context.Background(), "/source.txt", "/dest.txt"); err != nil {
		t.Fatal(err)
	}
	if data, _ := s.Read("/source.txt"); string(data) != "external new source" {
		t.Fatal("recreated source was lost")
	}
	if data, _ := s.Read("/dest.txt"); string(data) != "moving" {
		t.Fatal("destination not preserved")
	}
	for _, deleted := range s.Deletes() {
		if deleted == "/source.txt" {
			t.Fatal("source canonical path was deleted")
		}
	}
}

func TestMoveBackupFailureWithIdenticalBytesIsNotSuccess(t *testing.T) {
	s := testevs.New(t)
	s.Add("/source.txt", []byte("same"))
	s.Add("/dest.txt", []byte("same"))
	s.Configure(testevs.Options{FailBackup: true})
	c, _ := NewClient(s.Host, nil)
	if err := c.EnableRecovery(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer c.CloseRecovery()
	if err := c.MoveReplace(context.Background(), "/source.txt", "/dest.txt"); err == nil {
		t.Fatal("failed move reported success because target bytes already matched")
	}
	for _, name := range []string{"/source.txt", "/dest.txt"} {
		if data, ok := s.Read(name); !ok || string(data) != "same" {
			t.Fatalf("failed move changed %s", name)
		}
	}
}
