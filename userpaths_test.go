package colorer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasHRD(t *testing.T, s *Session, name string) bool {
	t.Helper()
	instances, err := s.EnumHRDInstances("rgb")
	if err != nil {
		t.Fatalf("EnumHRDInstances: %v", err)
	}
	for _, inst := range instances {
		if inst.Name == name {
			return true
		}
	}
	return false
}

// A folder of .hrd files is the form that has no path limits: each file names
// itself, and the mount of the folder is all Colorer needs to read it.
func TestUserHRD_FolderAddsColourStyles(t *testing.T) {
	configs := verifyConfigsOnHost(t)
	user := t.TempDir()
	writeFile(t, filepath.Join(user, "mine.hrd"), `<?xml version="1.0" encoding="UTF-8"?>
<hrd xmlns="http://colorer.sf.net/2003/hrd" class="rgb" name="user-folder-style" description="User folder style">
  <assign name="def:Text" fore="#123456" back="#654321"/>
</hrd>
`)

	s, err := NewSession(context.Background(), "/base/catalog.xml", configs, WithUserHRD(user))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	if !hasHRD(t, s, "user-folder-style") {
		t.Fatal("the user's colour style is not listed")
	}
	if !hasHRD(t, s, "default") {
		t.Fatal("the catalog's own colour styles are gone")
	}
	if err := s.SetHRD("rgb", "user-folder-style"); err != nil {
		t.Fatalf("SetHRD on the user's style: %v", err)
	}
	rd, err := s.GetRegionDefine("def:Text")
	if err != nil {
		t.Fatalf("GetRegionDefine: %v", err)
	}
	if !rd.IsForeSet || rd.Fore != 0x123456 || !rd.IsBackSet || rd.Back != 0x654321 {
		t.Errorf("def:Text = %+v, want the colours the user's file assigns", rd)
	}
}

// An <hrd-sets> file is FarColorer's "user file of color styles". Its links
// resolve against catalog.xml, which is what Colorer itself does.
func TestUserHRD_HrdSetsFileLinksRelativeToCatalog(t *testing.T) {
	configs := verifyConfigsOnHost(t)
	file := filepath.Join(t.TempDir(), "userhrd.xml")
	writeFile(t, file, `<?xml version="1.0" encoding="UTF-8"?>
<hrd-sets>
  <hrd class="rgb" name="user-sets-style" description="User sets style">
    <location link="hrd/rgb/black.hrd"/>
  </hrd>
</hrd-sets>
`)

	s, err := NewSession(context.Background(), "/base/catalog.xml", configs, WithUserHRD(file))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	if !hasHRD(t, s, "user-sets-style") {
		t.Fatal("the style from the user's hrd-sets file is not listed")
	}
	if err := s.SetHRD("rgb", "user-sets-style"); err != nil {
		t.Fatalf("SetHRD on the user's style: %v", err)
	}
}

// A user scheme is found by SelectType and parses, as FarColorer's "user file
// of schemes" would.
func TestUserHRC_FolderAddsFileTypes(t *testing.T) {
	configs := verifyConfigsOnHost(t)
	user := t.TempDir()
	writeFile(t, filepath.Join(user, "usertest.hrc"), `<?xml version="1.0" encoding="UTF-8"?>
<hrc version="take5" xmlns="http://colorer.sf.net/2003/hrc">
  <prototype name="usertest" group="user" description="User test language">
    <location link="usertest.hrc"/>
    <filename>/\.usertest$/</filename>
  </prototype>
  <type name="usertest">
    <import type="def"/>
    <scheme name="usertest">
      <keywords region="def:Keyword">
        <word name="frobnicate"/>
      </keywords>
    </scheme>
  </type>
</hrc>
`)

	s, err := NewSession(context.Background(), "/base/catalog.xml", configs, WithUserHRC(user))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	if err := s.SetHRD("rgb", "default"); err != nil {
		t.Fatalf("SetHRD: %v", err)
	}
	ok, err := s.SelectType("sample.usertest", "")
	if err != nil || !ok {
		t.Fatalf("SelectType on the user's file type: %v, %v", ok, err)
	}
	regions, err := s.ParseLine("  frobnicate")
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	found := false
	for _, r := range regions {
		if r.Start == 2 && r.End == 12 {
			found = true
		}
	}
	if !found {
		t.Errorf("the user's keyword was not highlighted; regions: %+v", regions)
	}
}

// A mistyped path must not stop highlighting: it is skipped and said so.
func TestUserPaths_MissingPathIsReportedAndSkipped(t *testing.T) {
	configs := verifyConfigsOnHost(t)
	missing := filepath.Join(t.TempDir(), "no-such-folder")
	rec := &diagnosticRecorder{}

	s, err := NewSession(context.Background(), "/base/catalog.xml", configs,
		WithDiagnostics(LevelWarn, rec.handle), WithUserHRC(missing), WithUserHRD(missing))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	for _, what := range []string{"user schemes not loaded", "user colour styles not loaded"} {
		if _, ok := rec.find(LevelWarn, what); !ok {
			rec.dump(t)
			t.Errorf("no warning %q", what)
		}
	}
	if ok, err := s.SelectType("test.json", ""); err != nil || !ok {
		t.Errorf("the catalog's own types stopped working: %v, %v", ok, err)
	}
}

// A user file Colorer cannot read fails the session with the host path named,
// and the FatalError still reachable for the throw site.
func TestUserHRC_BrokenFileNamesTheHostPath(t *testing.T) {
	configs := verifyConfigsOnHost(t)
	user := t.TempDir()
	writeFile(t, filepath.Join(user, "broken.hrc"), `<hrc><prototype name="x">`)

	s, err := NewSession(context.Background(), "/base/catalog.xml", configs, WithUserHRC(user))
	if err == nil {
		s.Close()
		t.Skip("Colorer accepted the broken file; nothing to report")
	}
	var fe *FatalError
	if !errors.As(err, &fe) {
		t.Fatalf("got %v (%T), want an error wrapping *FatalError", err, err)
	}
	if fe.Op != "colorer_load_user_hrc" {
		t.Errorf("Op = %q, want colorer_load_user_hrc", fe.Op)
	}
	if !strings.Contains(err.Error(), user) {
		t.Errorf("error %q does not name the host path %q", err, user)
	}
}

// Colorer's legacy strings cannot open these names; handing them over used to
// abort the session. They are refused up front, and the folder holding the
// files may itself be named anything.
func TestUserPaths_NonASCIINamesAreRefusedNotFatal(t *testing.T) {
	configs := verifyConfigsOnHost(t)
	folder := filepath.Join(t.TempDir(), "\u041f\u0430\u043f\u043a\u0430")
	style := `<hrd xmlns="http://colorer.sf.net/2003/hrd" class="rgb" name="user-ascii" description="x"/>`
	writeFile(t, filepath.Join(folder, "ascii.hrd"), style)

	s, err := NewSession(context.Background(), "/base/catalog.xml", configs, WithUserHRD(folder))
	if err != nil {
		t.Fatalf("an ASCII file in a non-ASCII folder: %v", err)
	}
	if !hasHRD(t, s, "user-ascii") {
		t.Error("the style in a non-ASCII folder is not listed")
	}
	s.Close()

	bad := filepath.Join(folder, "\u0418\u0432\u0430\u043d.hrd")
	writeFile(t, bad, style)
	for _, path := range []string{folder, bad} {
		rec := &diagnosticRecorder{}
		s, err := NewSession(context.Background(), "/base/catalog.xml", configs,
			WithDiagnostics(LevelWarn, rec.handle), WithUserHRD(path))
		if err != nil {
			t.Fatalf("%q stopped the session: %v", path, err)
		}
		if _, ok := rec.find(LevelWarn, "not ASCII"); !ok {
			rec.dump(t)
			t.Errorf("%q was not reported as a name Colorer cannot open", path)
		}
		s.Close()
	}
}
