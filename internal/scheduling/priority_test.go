package scheduling

import "testing"

func TestPriorityClassForJobPriority(t *testing.T) {
	cases := map[string]string{
		"critical": PriorityClassCritical,
		"high":     PriorityClassHigh,
		"medium":   PriorityClassMedium,
		"low":      PriorityClassLow,
		"":         "",
		"HIGH":     "", // the job enum is lower-case; the service enum is upper
		"nonsense": "",
	}
	for in, want := range cases {
		if got := PriorityClassForJobPriority(in); got != want {
			t.Errorf("PriorityClassForJobPriority(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPriorityClassForServiceClass(t *testing.T) {
	cases := map[string]string{
		"HIGH":     PriorityClassHigh,
		"MEDIUM":   PriorityClassMedium,
		"LOW":      PriorityClassLow,
		"":         "",
		"high":     "", // the service enum is upper-case
		"CRITICAL": "", // deliberately not a serviceClass value
	}
	for in, want := range cases {
		if got := PriorityClassForServiceClass(in); got != want {
			t.Errorf("PriorityClassForServiceClass(%q) = %q, want %q", in, got, want)
		}
	}
}
