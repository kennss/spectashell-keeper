// hostkeeper_test.go
// Author: Kennt Kim
// Created: 2026-10-26 · Last updated: 2026-10-29
//
// Purpose: which build a keeper program's path is (BuildOf), which the installer keeps a folder for
// while a program of that build runs. Moved here from the core's keeperssh_test.go on 2026-10-29.

package hostkeeper

import "testing"

func TestBuildOf(t *testing.T) {
	home := "/home/u"
	for path, want := range map[string]string{
		"/home/u/.local/share/sdterm/0.1.0+1.7c72/sdterm-keeper":           "0.1.0+1.7c72",
		"/home/u/.local/share/sdterm/0.1.0+1.7c72/sdterm-keeper (deleted)": "0.1.0+1.7c72",
		"/home/u/.local/share/sdterm/current/sdterm-keeper":                "current",
		"/home/u/.local/share/sdterm/sdterm-keeper":                        "",
		"/home/u/.local/share/sdtermx/b/sdterm-keeper":                     "",
		"/Applications/SpectaShell.app/Contents/Helpers/sdterm-keeper":     "",
		"/home/u/.local/share/sdterm/a b/sdterm-keeper":                    "",
		"": "",
	} {
		if got := BuildOf(home, path); got != want {
			t.Errorf("BuildOf(%q) = %q, want %q", path, got, want)
		}
	}
}
