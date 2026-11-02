// install_test.go
// Author: Kennt Kim
// Created: 2026-11-02 · Last updated: 2026-11-02
//
// Purpose: Install leaves the directory holding the scripts and nothing else (shellinteg.go's
// notes): fish reads every file in vendor_conf.d, so a script an earlier build wrote under another
// name — sdterm.fish, before the scripts took SpectaShell's name — would mark each prompt and
// command a second time.

package shellinteg

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestInstallLeavesOnlyTheScripts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shell-integration")
	earlier := []string{"fish/vendor_conf.d/sdterm.fish", "bash/sdterm.bash", "zsh/sdterm.zsh",
		"zsh/spectashell.zsh.tmp", "gone/old.txt"}
	for _, name := range earlier {
		file := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("echo earlier\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := Install(dir); err != nil {
		t.Fatal(err)
	}
	var got []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if rel != "." {
			if d.IsDir() {
				rel += "/"
			}
			got = append(got, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"bash/", "bash/spectashell.bash", "fish/", "fish/vendor_conf.d/",
		"fish/vendor_conf.d/spectashell.fish", "zsh/", "zsh/.zshenv", "zsh/spectashell.zsh"}
	if !slices.Equal(got, want) {
		t.Fatalf("the directory holds %q, want only the scripts %q", got, want)
	}
}

func TestInstallWithNothingThereYet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "shell-integration")
	if err := Install(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fish", "vendor_conf.d", "spectashell.fish")); err != nil {
		t.Fatal(err)
	}
}
