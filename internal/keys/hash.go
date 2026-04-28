package keys

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters: tuned for cheap-enough verify on every cache miss.
// 32 MB memory, 1 pass, 2 lanes — well above OWASP "low memory" floor.
const (
	argonTime    = 1
	argonMemory  = 32 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

func hashKey(plain string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$%s$%s", hex.EncodeToString(salt), hex.EncodeToString(sum)), nil
}

func verifyHash(plain, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 3 || parts[0] != "argon2id" {
		return false
	}
	salt, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	expected, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, uint32(len(expected)))
	return subtle.ConstantTimeCompare(sum, expected) == 1
}
