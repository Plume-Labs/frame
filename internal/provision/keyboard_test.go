package provision

import "testing"

func TestKeyboardDefaultsToUSAndRendersTheChosenLayout(t *testing.T) {
	kb := func(s Spec) string {
		ai := parsed(t, s, "")
		return ai["keyboard"].(map[string]any)["layout"].(string)
	}
	if got := kb(goodSpec()); got != "us" {
		t.Errorf("default layout = %s", got)
	}
	s := goodSpec()
	s.Keyboard = "fr"
	if got := kb(s); got != "fr" {
		t.Errorf("layout = %s", got)
	}
}

func TestKeyboardOutsideTheListIsRefused(t *testing.T) {
	for _, k := range []string{"fr\nx", "FR", "xx", "fr; reboot"} {
		s := goodSpec()
		s.Keyboard = k
		if err := ValidateSpec(s); err == nil {
			t.Errorf("keyboard %q accepted", k)
		}
	}
	for _, k := range KeyboardLayouts() {
		s := goodSpec()
		s.Keyboard = k
		if err := ValidateSpec(s); err != nil {
			t.Errorf("keyboard %q refused: %v", k, err)
		}
	}
}
