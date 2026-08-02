// Package crypto defines the e2ee seam kage's messaging path is built
// against. gpg is the only implementation today; OMEMO can be added later as
// a second Encrypter without reshaping the send/receive call sites.
package crypto

// Encrypter turns plaintext message bodies into ciphertext and back, keyed
// by whatever identifier the implementation needs to find the right key
// (a GPG key fingerprint, an OMEMO device list, etc).
type Encrypter interface {
	Name() string
	Encrypt(plaintext string, recipientKeyID string) (string, error)
	Decrypt(ciphertext string, senderKeyID string) (string, error)
}
