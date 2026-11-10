package print

import "testing"

func TestColorEnabledEnv(t *testing.T) {
	env := func(v *string) func(string) (string, bool) {
		return func(string) (string, bool) {
			if v == nil {
				return "", false
			}
			return *v, true
		}
	}
	empty, set := "", "1"
	cases := []struct {
		name string
		tty  bool
		env  *string
		want bool
	}{
		{"terminal, unset", true, nil, true},
		{"terminal, empty", true, &empty, false},
		{"terminal, set", true, &set, false},
		{"not a terminal, unset", false, nil, false},
	}
	for _, tc := range cases {
		if got := colorEnabled(tc.tty, env(tc.env)); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
