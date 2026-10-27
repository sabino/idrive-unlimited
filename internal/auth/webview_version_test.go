package auth

import "testing"

func TestMissingOrStaleWebViewRegistration(t *testing.T) {
	for _, v := range []string{"", "0.0.0.0", "Microsoft Edge WebView2 Runtime", "-1.2.3.4", "1.2.3"} {
		if validWebViewVersion(v) {
			t.Fatalf("accepted unusable runtime version %q", v)
		}
	}
	if !validWebViewVersion("150.0.1234.5") {
		t.Fatal("rejected runtime version")
	}
}
