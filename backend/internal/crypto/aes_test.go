package crypto

import "testing"

func TestEncryptDecrypt(t *testing.T) {
	c, err := New("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	ct, err := c.Encrypt("sk-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	pt, err := c.Decrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	if pt != "sk-test-secret" {
		t.Fatalf("got %q", pt)
	}
}

func TestKeyPreview(t *testing.T) {
	got := KeyPreview("sk-abcdefghijklmnop")
	if got != "sk-abc...mnop" {
		t.Fatalf("got %q", got)
	}
}
