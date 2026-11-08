package rclone

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

// obscureKey is the fixed, public key rclone's own obscuring uses
// (fs/config/obscure). Obscuring is not encryption; it only keeps a secret from
// being read at a glance. Anything here must stay compatible with `rclone reveal`.
var obscureKey = []byte{
	0x9c, 0x93, 0x5b, 0x48, 0x73, 0x0a, 0x55, 0x4d,
	0x6b, 0xfd, 0x7c, 0x63, 0xc8, 0x86, 0xa9, 0x2b,
	0xd3, 0x90, 0x19, 0x8e, 0xb8, 0x12, 0x8a, 0xfb,
	0xf4, 0xde, 0x16, 0x2b, 0x8b, 0x95, 0xf6, 0x38,
}

// Obscure converts a plain secret into the form rclone expects in config:
// base64url(IV || AES-CTR(plain)), the same format as `rclone obscure`.
//
// Unlike `rclone obscure`, the IV is derived from the secret instead of being
// random, so the same secret always gives the same string. That matters:
// rclone names its on-disk VFS cache folder after a hash of the remote's
// config, so a random IV gave every start a new, empty cache folder and left
// files that were written but not yet uploaded stranded in the old one.
func Obscure(plain string) string {
	mac := hmac.New(sha256.New, obscureKey)
	mac.Write([]byte("foxcloud obscure iv v1\x00"))
	mac.Write([]byte(plain))
	iv := mac.Sum(nil)[:aes.BlockSize]

	block, err := aes.NewCipher(obscureKey)
	if err != nil { // the key length is fixed, so this cannot happen
		panic(err)
	}
	buf := make([]byte, aes.BlockSize+len(plain))
	copy(buf, iv)
	cipher.NewCTR(block, iv).XORKeyStream(buf[aes.BlockSize:], []byte(plain))
	return base64.RawURLEncoding.EncodeToString(buf)
}
