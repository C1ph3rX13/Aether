package spec

import "testing"

func TestResultFinalizationPhaseSecondsIsBounded(t *testing.T) {
	for _, test := range []struct {
		phase int
		want  int
	}{
		{phase: -1, want: 0},
		{phase: 0, want: 0},
		{phase: 5, want: 5},
		{phase: 30, want: 30},
		{phase: 31, want: 30},
	} {
		if got := ResultFinalizationPhaseSeconds(test.phase); got != test.want {
			t.Fatalf("ResultFinalizationPhaseSeconds(%d)=%d, want %d", test.phase, got, test.want)
		}
	}
}
