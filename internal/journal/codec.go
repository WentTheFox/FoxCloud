// Package journal lets machines tell each other what they changed, so a
// machine learns of another's uploads without listing the whole remote.
//
// Every machine appends one encrypted line per change to a shared log on the
// server, outside the synced tree. Others follow that log and fetch only the
// files it names. The log is a hint and never the truth: a reader checks the
// real state of what a record names, and any sign that a record may have been
// missed (a gap in a machine's sequence, a line that does not decrypt, a reset
// log) means "do a full scan", not "trust it anyway".
package journal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// Operations a record can carry.
const (
	OpUpload = "up"    // a file was created or changed
	OpMkdir  = "mkdir" // an (empty) directory was created
	OpMove   = "mv"    // a file or directory was renamed: Path -> To
	OpDelete = "del"   // a file was removed
	OpRmdir  = "rmdir" // a directory and everything in it was removed
)

// Record is one change. Paths are as they appear in the remote's crypt root
// (forward slashes, no leading slash), so machines whose local folders differ
// still agree on what changed.
type Record struct {
	Machine string `json:"m"`
	// Gen identifies one life of a machine's journal state; a new Gen means the
	// machine lost its counter, so earlier sequence numbers say nothing.
	Gen  string `json:"g"`
	Seq  uint64 `json:"n"`
	Time int64  `json:"t"` // unix seconds, informational only
	Op   string `json:"o"`
	Path string `json:"p"`
	To   string `json:"to,omitempty"`
	Dir  bool   `json:"d,omitempty"`
	Size int64  `json:"s,omitempty"`
	Mod  int64  `json:"mt,omitempty"` // modification time, unix nanoseconds
}

// Codec encrypts and decrypts records with a key derived from the crypt master
// key, so the server sees neither names nor machine activity in the clear.
type Codec struct{ aead cipher.AEAD }

// NewCodec derives the journal key from the master key.
func NewCodec(master string) (*Codec, error) {
	if master == "" {
		return nil, errors.New("empty crypt key")
	}
	key, err := hkdf.Key(sha256.New, []byte(master), nil, "foxcloud journal v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Codec{aead}, nil
}

// Encode turns a record into one text line (no newline): base64 of a random
// nonce followed by the authenticated ciphertext.
func (c *Codec) Encode(r Record) (string, error) {
	plain, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(c.aead.Seal(nonce, nonce, plain, nil)), nil
}

// ErrBadLine means a line did not decrypt: torn by a concurrent write, cut
// short, or not ours. The caller treats it as a possible missed change.
var ErrBadLine = errors.New("journal line did not decrypt")

// Decode reads a line written by Encode.
func (c *Codec) Decode(line string) (Record, error) {
	var r Record
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(line))
	if err != nil || len(raw) < c.aead.NonceSize()+c.aead.Overhead() {
		return r, ErrBadLine
	}
	n := c.aead.NonceSize()
	plain, err := c.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return r, ErrBadLine
	}
	if err := json.Unmarshal(plain, &r); err != nil {
		return r, ErrBadLine
	}
	return r, nil
}
