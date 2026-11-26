package github

import "testing"

func TestSignature(t *testing.T) {
	// The example from GitHub's documentation on validating deliveries.
	secret, body := []byte("It's a Secret to Everybody"), []byte("Hello, World!")
	const want = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	if got := Sign(secret, body); got != want {
		t.Fatalf("Sign = %s", got)
	}
	if !ValidSignature(secret, body, want) {
		t.Fatal("GitHub's example was refused")
	}
	for _, bad := range []string{
		"", "sha256=", want[7:], "sha1=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17",
		"sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e18",
		"sha256=757107EA0EB2509FC211221CCE984B8A37570B6D7586C22C46F4379C8B043E1Z",
		want + "00",
	} {
		if ValidSignature(secret, body, bad) {
			t.Errorf("accepted %q", bad)
		}
	}
	if ValidSignature([]byte("other"), body, want) || ValidSignature(secret, []byte("Hello, World?"), want) {
		t.Fatal("wrong secret or body accepted")
	}
}

func TestValidCommitAndName(t *testing.T) {
	if !ValidCommitSHA("6113728f27ae82c7b1a177c8d03f9e96e0adf246") || ValidCommitSHA("6113728F27AE82C7B1A177C8D03F9E96E0ADF246") ||
		ValidCommitSHA("6113728") || ValidCommitSHA("") {
		t.Fatal("commit ids")
	}
	for s, want := range map[string]bool{
		"acme/site": true, "a-b/c.d_e-1": true, "acme": false, "acme/": false, "/site": false, "acme/..": false,
		"acme/si te": false, "-acme/site": false, "acme/site/x": false, "acme/<b>": false,
	} {
		if ValidFullName(s) != want {
			t.Errorf("ValidFullName(%q) != %v", s, want)
		}
	}
}
