//go:build windows

package secret

import (
	"bytes"
	"testing"
)

func TestSealOpenRoundtrip(t *testing.T) {
	plain := []byte(`{"appSecret":"45uWv65rQlrjNxLW"}`)
	cipher, err := Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(cipher, plain) {
		t.Fatal("密文不应等于明文")
	}
	if !bytes.HasPrefix(cipher, sealPrefix) {
		t.Fatal("密文应带 DPAPI 前缀")
	}
	got, sealed, err := Open(cipher)
	if err != nil || !sealed {
		t.Fatalf("Open: sealed=%v err=%v", sealed, err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip 不一致: %q", got)
	}
}

func TestOpenLegacyPlaintext(t *testing.T) {
	plain := []byte(`{"token":"abc"}`)
	got, sealed, err := Open(plain)
	if err != nil {
		t.Fatal(err)
	}
	if sealed {
		t.Fatal("旧明文应返回 sealed=false")
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("旧明文应原样返回: %q", got)
	}
}

func TestOpenGarbageFails(t *testing.T) {
	bad := append([]byte{}, sealPrefix...)
	bad = append(bad, []byte("不是合法密文")...)
	if _, _, err := Open(bad); err == nil {
		t.Fatal("损坏密文应报错")
	}
}
