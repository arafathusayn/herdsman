package shell

import (
	"os"
	"path/filepath"
	"testing"
)

func fileAt(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestUnknownShellsAreRefused(t *testing.T) {
	if _, err := Install("csh", Env{Home: t.TempDir()}); err == nil {
		t.Fatal("Install")
	}
	if _, err := Uninstall("csh", Env{Home: t.TempDir()}); err == nil {
		t.Fatal("Uninstall")
	}
}

func TestAStartupFileThatCannotBeReadIsReported(t *testing.T) {
	home := t.TempDir()
	os.Mkdir(filepath.Join(home, ".bashrc"), 0o755) // A folder where the file goes.
	if _, err := Install("bash", Env{Home: home}); err == nil {
		t.Fatal("Install")
	}
	if _, err := Uninstall("bash", Env{Home: home}); err == nil {
		t.Fatal("Uninstall")
	}
}

func TestAnUnfinishedBlockIsLeftForAPerson(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".bashrc")
	os.WriteFile(path, []byte(startMark+"\n"), 0o644)
	if _, err := Install("bash", Env{Home: home}); err == nil {
		t.Fatal("Install")
	}
	if _, err := Uninstall("bash", Env{Home: home}); err == nil {
		t.Fatal("Uninstall")
	}
}

func TestAStartupFolderThatCannotBeMadeIsReported(t *testing.T) {
	if _, err := Install("fish", Env{Home: fileAt(t)}); err == nil {
		t.Fatal("Install")
	}
}

func TestAStartupFolderThatCannotBeWrittenIsReported(t *testing.T) {
	home := t.TempDir()
	os.Chmod(home, 0o500)
	defer os.Chmod(home, 0o700)
	if _, err := Install("bash", Env{Home: home}); err == nil {
		t.Fatal("Install")
	}
}

func TestUninstallWithNoBlockChangesNothing(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".bashrc")
	os.WriteFile(path, []byte("mine\n"), 0o644)
	if found, err := Uninstall("bash", Env{Home: home}); found || err != nil {
		t.Fatalf("%v %v", found, err)
	}
}
