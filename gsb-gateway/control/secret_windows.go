package main

import (
	"encoding/base64"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DPAPI binds the ciphertext to the current Windows user, so a copied
// config.json is useless on another account or machine.

func encryptSecret(plain string) (string, error) {
	out, err := dpapiCall([]byte(plain), true)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

func decryptSecret(enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	out, err := dpapiCall(raw, false)
	return string(out), err
}

func dpapiCall(data []byte, protect bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	const flags = windows.CRYPTPROTECT_UI_FORBIDDEN
	var err error
	if protect {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, flags, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, flags, &out)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
