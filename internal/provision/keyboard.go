package provision

import (
	"fmt"
	"slices"
)

// KeyboardLayouts are the console layouts an installation can be given. A fixed
// list, like the roles: the value is written into the autoinstall document.
func KeyboardLayouts() []string {
	return []string{"us", "fr", "be", "gb", "de", "es", "it"}
}

func validateKeyboard(k string) error {
	if k == "" || slices.Contains(KeyboardLayouts(), k) {
		return nil
	}
	return fmt.Errorf("spec: keyboard %q must be empty or one of %v", k, KeyboardLayouts())
}

func keyboardOrUS(k string) string {
	if k == "" {
		return "us"
	}
	return k
}
