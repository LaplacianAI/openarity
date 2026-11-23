package proposals

import (
	"slices"
	"testing"
)

// A quality change saves nothing, an efficiency change names cost, time or
// both, and nothing else is a goal or a saving.
func TestAGoalAndItsSavingAreHeldTogether(t *testing.T) {
	for _, tc := range []struct {
		goal  string
		saves []string
		want  []string
		ok    bool
	}{
		{"", nil, []string{}, true},
		{GoalQuality, nil, []string{}, true},
		{GoalEfficiency, []string{SavesTime, SavesCost, SavesTime}, []string{SavesCost, SavesTime}, true},
		{"", []string{SavesCost}, nil, false},
		{GoalQuality, []string{SavesCost}, nil, false},
		{GoalEfficiency, nil, nil, false},
		{GoalEfficiency, []string{"money"}, nil, false},
		{"speed", []string{SavesTime}, nil, false},
	} {
		goal, saves, err := goalOf(Proposed{Goal: tc.goal, Saves: tc.saves})
		if (err == nil) != tc.ok {
			t.Errorf("%q %v: err = %v, want ok %v", tc.goal, tc.saves, err, tc.ok)
			continue
		}
		if tc.ok && (!slices.Equal(saves, tc.want) || (goal != GoalQuality && goal != GoalEfficiency)) {
			t.Errorf("%q %v = %q %v, want %v", tc.goal, tc.saves, goal, saves, tc.want)
		}
		if !tc.ok && !Refused(err) {
			t.Errorf("%q %v: %v is not a refusal", tc.goal, tc.saves, err)
		}
	}
}
