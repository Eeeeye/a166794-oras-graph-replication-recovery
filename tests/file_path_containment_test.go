/*
Copyright The ORAS Authors.
Licensed under the Apache License, Version 2.0.
*/

package file

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type talentsTarEntry struct {
	name     string
	linkname string
	body     []byte
	typeflag byte
	mode     int64
}

func talentsBuildTar(t *testing.T, entries []talentsTarEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	for _, entry := range entries {
		mode := entry.mode
		if mode == 0 {
			mode = 0644
		}
		h := &tar.Header{
			Name:     entry.name,
			Linkname: entry.linkname,
			Typeflag: entry.typeflag,
			Mode:     mode,
			Size:     int64(len(entry.body)),
		}
		if entry.typeflag != tar.TypeReg && entry.typeflag != tar.TypeRegA {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if len(entry.body) != 0 {
			if _, err := tw.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestTalentsNamedWriteRejectsSymlinkAncestorEscape(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	storeRoot := filepath.Join(base, "store")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storeRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(storeRoot, "out")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	store, err := New(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.resolveWritePath("out/secret.txt"); !errors.Is(err, ErrPathTraversalDisallowed) {
		t.Fatalf("resolveWritePath() error = %v, want %v", err, ErrPathTraversalDisallowed)
	}
	got, err := store.resolveWritePath("safe/new/file.txt")
	if err != nil {
		t.Fatalf("ordinary in-store path failed: %v", err)
	}
	if want := filepath.Join(storeRoot, "safe/new/file.txt"); got != want {
		t.Fatalf("ordinary path = %q, want %q", got, want)
	}
}

func TestTalentsTarRejectsResolvedSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	dirPath := filepath.Join(base, "extract", "base")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dirPath, "out")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	tarData := talentsBuildTar(t, []talentsTarEntry{{
		name: "base/out", typeflag: tar.TypeDir, mode: 0700,
	}})
	if err := extractTarDirectory(dirPath, "base", bytes.NewReader(tarData), make([]byte, 1024), true); !errors.Is(err, ErrPathTraversalDisallowed) {
		t.Fatalf("extractTarDirectory() error = %v, want %v", err, ErrPathTraversalDisallowed)
	}
}

func TestTalentsTarHardlinksResolveInsideExtractionRoot(t *testing.T) {
	cwd := t.TempDir()
	sentinel := filepath.Join(cwd, "target.txt")
	if err := os.WriteFile(sentinel, []byte("outside secret"), 0600); err != nil {
		t.Fatal(err)
	}
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldCWD); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	}()

	dirPath := filepath.Join(t.TempDir(), "base")
	tarData := talentsBuildTar(t, []talentsTarEntry{
		{name: "base/", typeflag: tar.TypeDir, mode: 0755},
		{name: "base/target.txt", typeflag: tar.TypeReg, mode: 0644, body: []byte("inside")},
		{name: "base/copy.txt", typeflag: tar.TypeLink, mode: 0644, linkname: "target.txt"},
	})
	if err := extractTarDirectory(dirPath, "base", bytes.NewReader(tarData), make([]byte, 1024), false); err != nil {
		t.Fatalf("valid in-tree relative hardlink failed: %v", err)
	}
	targetInfo, err := os.Stat(filepath.Join(dirPath, "target.txt"))
	if err != nil {
		t.Fatal(err)
	}
	copyInfo, err := os.Stat(filepath.Join(dirPath, "copy.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(targetInfo, copyInfo) {
		t.Fatal("in-tree hardlink does not share the target inode")
	}
	outsideInfo, err := os.Stat(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(copyInfo, outsideInfo) {
		t.Fatal("hardlink resolved against process CWD and escaped extraction root")
	}
}

func TestTalentsTarHardlinkCannotImportCWDFile(t *testing.T) {
	cwd := t.TempDir()
	sentinel := filepath.Join(cwd, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldCWD) //nolint:errcheck

	dirPath := filepath.Join(t.TempDir(), "base")
	tarData := talentsBuildTar(t, []talentsTarEntry{
		{name: "base/", typeflag: tar.TypeDir, mode: 0755},
		{name: "base/evil.txt", typeflag: tar.TypeLink, mode: 0644, linkname: "sentinel.txt"},
	})
	err = extractTarDirectory(dirPath, "base", bytes.NewReader(tarData), make([]byte, 1024), false)
	if err == nil {
		t.Fatal("missing in-root hardlink target was accepted")
	}
	if _, statErr := os.Lstat(filepath.Join(dirPath, "evil.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("failed extraction left an escaped hardlink: %v", statErr)
	}
}
