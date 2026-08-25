package config

import (
	"reflect"
	"testing"
)

func TestSplitAndTrim(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"A", []string{"A"}},
		{"A,B,C", []string{"A", "B", "C"}},
		{" A , B ,, C ", []string{"A", "B", "C"}},
	}
	for _, c := range cases {
		got := splitAndTrim(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitAndTrim(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}
