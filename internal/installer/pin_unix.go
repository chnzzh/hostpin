//go:build !windows

package installer

import (
	"fmt"
	"os"
)

func validatePINFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("PIN file %s must have mode 0600", path)
	}
	return nil
}
