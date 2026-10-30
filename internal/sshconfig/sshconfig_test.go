package sshconfig

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	out := "user alice\nhostname box.example.test\nport 23\nidentitiesonly no\n" +
		"identityfile ~/.ssh/key_a\nidentityfile ~/.ssh/key_b\n" +
		"userknownhostsfile /h/.ssh/known_hosts /h/.ssh/known_hosts2\n"
	want := Result{
		Host: "box.example.test", Port: 23, User: "alice",
		IdentityFiles: []string{"~/.ssh/key_a", "~/.ssh/key_b"},
		KnownHosts:    []string{"/h/.ssh/known_hosts", "/h/.ssh/known_hosts2"},
	}
	if got := Parse(out); !reflect.DeepEqual(got, want) {
		t.Errorf("Parse() = %+v, want %+v", got, want)
	}
}
