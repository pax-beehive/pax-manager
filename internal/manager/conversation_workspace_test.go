package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsSupportedSessionWorkspace(t *testing.T) {
	tests := []struct {
		name      string
		workspace string
		expected  bool
	}{
		{name: "given empty optional workspace", workspace: "", expected: true},
		{name: "given absolute workspace", workspace: "/workspace/project", expected: true},
		{name: "given home workspace", workspace: "~", expected: true},
		{name: "given workspace below home", workspace: "~/project", expected: true},
		{name: "given ordinary relative workspace", workspace: "project", expected: false},
		{name: "given current directory workspace", workspace: "./project", expected: false},
		{name: "given parent directory workspace", workspace: "../project", expected: false},
		{name: "given another users home", workspace: "~alice/project", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isSupportedSessionWorkspace(tt.workspace))
		})
	}
}
