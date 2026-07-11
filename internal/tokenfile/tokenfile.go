// Package tokenfile reads bearer tokens from files — daemons take a
// -token-file flag rather than the token itself, which would leak via the
// process list and shell history.
package tokenfile

import (
	"fmt"
	"os"
	"strings"
)

// Read returns the token in path with surrounding whitespace trimmed. An
// empty path returns "" (auth disabled); an existing-but-blank file is an
// error, since it silently means "no auth" where auth was intended.
func Read(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return token, nil
}
