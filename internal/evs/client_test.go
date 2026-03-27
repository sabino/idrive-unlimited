package evs

import "testing"

func TestCleanPath(t *testing.T) {
	tests := map[string]string{
		"":                 "/",
		"/":                "/",
		"folder":           "/folder",
		"folder\\child":    "/folder/child",
		"/folder/../child": "/child",
		" //folder//a ":    "/folder/a",
	}

	for input, want := range tests {
		if got := CleanPath(input); got != want {
			t.Fatalf("CleanPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParentDir(t *testing.T) {
	tests := map[string]string{
		"/":          "/",
		"/folder":    "/",
		"/a/b/c.txt": "/a/b",
	}

	for input, want := range tests {
		if got := ParentDir(input); got != want {
			t.Fatalf("ParentDir(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsReadOnlyPath(t *testing.T) {
	readOnly := []string{
		"/device/Contacts",
		"/device/Calendar/item",
		"/device/Call Logs",
		"/device/SMS/thread",
	}
	for _, input := range readOnly {
		if !IsReadOnlyPath(input) {
			t.Fatalf("expected %q to be read-only", input)
		}
	}

	writable := []string{
		"/",
		"/device/Test",
		"/Uploads/file.bin",
	}
	for _, input := range writable {
		if IsReadOnlyPath(input) {
			t.Fatalf("expected %q to be writable", input)
		}
	}
}
