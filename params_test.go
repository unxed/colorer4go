package colorer

import (
	"context"
	"path/filepath"
	"testing"
)

// far2l's plug/hrcsettings.xml, cut to the parameters FarColorer's list of
// types uses.
const testHRCSettings = `<?xml version="1.0" encoding="UTF-8"?>
<hrc-settings>
  <prototype name="default">
    <param name="hotkey" value="" description="Key assigned in the menu of file types"/>
    <param name="favorite" value="false" description="Shown among favourites"/>
  </prototype>
</hrc-settings>
`

func TestFileTypeParams_DefaultsAndUserValues(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "hrcsettings.xml")
	writeFile(t, settings, testHRCSettings)
	s, err := NewSession(context.Background(), "/base/catalog.xml", verifyConfigsOnHost(t), WithHRCSettings(settings))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	if v, ok, err := s.FileTypeParam("default", "favorite"); err != nil || !ok || v != "false" {
		t.Fatalf("default favorite = %q, %v, %v; want false from the settings file", v, ok, err)
	}
	if _, ok, _ := s.FileTypeParam("json", "favorite"); ok {
		t.Fatal("json has a favorite parameter before one was set")
	}
	if err := s.SetFileTypeParam("json", "favorite", "true"); err != nil {
		t.Fatalf("SetFileTypeParam: %v", err)
	}
	if err := s.SetFileTypeParam("json", "hotkey", "J"); err != nil {
		t.Fatalf("SetFileTypeParam: %v", err)
	}
	if v, ok, _ := s.FileTypeParam("json", "favorite"); !ok || v != "true" {
		t.Errorf("json favorite = %q, %v; want true", v, ok)
	}
	if v, _, _ := s.FileTypeParam("json", "hotkey"); v != "J" {
		t.Errorf("json hotkey = %q, want J", v)
	}
	if err := s.SetFileTypeParam("json", "no-such-param", "x"); err == nil {
		t.Error("set a parameter neither json nor default has")
	}
	if err := s.SetFileTypeParam("no-such-type", "favorite", "true"); err == nil {
		t.Error("set a parameter of a type that does not exist")
	}
}

func TestSetFileType_OverridesTheChoice(t *testing.T) {
	s, err := NewSession(context.Background(), "/base/catalog.xml", verifyConfigsOnHost(t))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()
	if err := s.SetHRD("rgb", "default"); err != nil {
		t.Fatalf("SetHRD: %v", err)
	}
	if name, _ := s.FileType(); name != "" {
		t.Fatalf("FileType before any = %q", name)
	}
	if ok, _ := s.SelectType("a.json", ""); !ok {
		t.Fatal("SelectType failed")
	}
	if name, _ := s.FileType(); name != "json" {
		t.Fatalf("FileType after SelectType(a.json) = %q, want json", name)
	}
	if ok, err := s.SetFileType("c"); err != nil || !ok {
		t.Fatalf("SetFileType(c) = %v, %v", ok, err)
	}
	if name, _ := s.FileType(); name != "c" {
		t.Errorf("FileType after SetFileType = %q, want c", name)
	}
	if ok, _ := s.SetFileType("no-such-type"); ok {
		t.Error("SetFileType accepted a type that does not exist")
	}
	regions, err := s.ParseLine("int x;")
	if err != nil || len(regions) == 0 {
		t.Errorf("parsing as C: %v, %d regions", err, len(regions))
	}
}
