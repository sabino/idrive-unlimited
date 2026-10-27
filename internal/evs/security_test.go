package evs

import (
	"context"
	"errors"
	"idrive-unlimited/internal/testevs"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRejectServerURLInjection(t *testing.T) {
	for _, host := range []string{"user:pass@host.idrive.com", "host.idrive.com/path", "host.idrive.com?q=1", "host.idrive.com#fragment", "https://host.idrive.com", "host.idrive.com/../other"} {
		if _, err := NewClient(host, nil); err == nil {
			t.Errorf("accepted %q", host)
		}
	}
	if _, err := NewClient("evs.idrive.com", nil); err != nil {
		t.Fatal(err)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestAPIResponseLimitAndRedaction(t *testing.T) {
	if _, err := readAPIResponse(io.LimitReader(zeros{}, (64<<20)+1)); err == nil {
		t.Fatal("accepted oversized response")
	}
	if err := checkHTTPStatus(500, []byte("secret-cookie")); err == nil || strings.Contains(err.Error(), "secret-cookie") {
		t.Fatalf("raw response disclosed: %v", err)
	}
}

func TestRawTrimmedPathsCannotMutateAnotherFile(t *testing.T) {
	s := testevs.New(t)
	c, _ := NewClient(s.Host, nil)
	ctx := context.Background()
	operations := []func() error{func() error { return c.Mkdir(ctx, "/unsafe ") }, func() error { return c.Delete(ctx, "/unsafe ") }, func() error { return c.Rename(ctx, "/unsafe ", "/other") }, func() error { return c.Copy(ctx, "/unsafe ", "/") }, func() error { return c.Move(ctx, "/unsafe ", "/") }, func() error { return c.MoveReplace(ctx, "/unsafe ", "/other") }, func() error { return c.UploadFile(ctx, "unused", "/parent /child", time.Now()) }}
	for _, operation := range operations {
		if err := operation(); !errors.Is(err, ErrUnsupportedPath) {
			t.Fatalf("unsafe path was not rejected: %v", err)
		}
	}
	if s.Requests() != 0 {
		t.Fatal("unsafe raw path reached the provider")
	}
}

func TestSpecialFolderNamesAreWritableOutsideDeviceRoots(t *testing.T) {
	s := testevs.New(t)
	s.Device("/device", "Phone")
	s.Directory("/ordinary")
	c, _ := NewClient(s.Host, nil)
	if err := c.Mkdir(context.Background(), "/ordinary/Contacts"); err != nil {
		t.Fatalf("normal user folder blocked: %v", err)
	}
	if err := c.Mkdir(context.Background(), "/device/Contacts"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("device special folder allowed: %v", err)
	}
	node, err := c.StatFresh(context.Background(), "/ordinary/Contacts")
	if err != nil || node.ReadOnly {
		t.Fatalf("normal folder reported read-only: %+v %v", node, err)
	}
}

func TestUnknownRootClassificationDoesNotPermitContainerDeletion(t *testing.T) {
	s := testevs.New(t)
	s.Directory("/unknown")
	s.Configure(testevs.Options{OmitDevices: true})
	c, _ := NewClient(s.Host, nil)
	if err := c.Delete(context.Background(), "/unknown"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("unknown root container deletion allowed: %v", err)
	}
	if len(s.Deletes()) != 0 {
		t.Fatal("deleted a container without classifying it")
	}
}
