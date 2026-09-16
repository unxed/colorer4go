package colorer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestFileTypes_ListsCatalogAndUserTypes(t *testing.T) {
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
    <scheme name="usertest"/>
  </type>
</hrc>
`)
	s, err := NewSession(context.Background(), "/base/catalog.xml", configs, WithUserHRC(user))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	types, err := s.FileTypes()
	if err != nil {
		t.Fatalf("FileTypes: %v", err)
	}
	byName := map[string]FileType{}
	for _, ft := range types {
		byName[ft.Name] = ft
	}
	if ft, ok := byName["json"]; !ok || ft.Description == "" || ft.Group == "" {
		t.Errorf("json = %+v, %v; want the catalog's JSON type with a group and a description", ft, ok)
	}
	if ft := byName["usertest"]; ft.Group != "user" || ft.Description != "User test language" {
		t.Errorf("usertest = %+v; want the user's type as declared", ft)
	}

	if ok, err := s.LoadFileType("usertest"); err != nil || !ok {
		t.Errorf("LoadFileType(usertest) = %v, %v; want a loaded scheme", ok, err)
	}
	if _, err := s.LoadFileType("no-such-type"); err == nil {
		t.Error("LoadFileType accepted a name no type has")
	}
	if s.Err() != nil {
		t.Errorf("an unknown name broke the session: %v", s.Err())
	}
}

func TestFileTypes_BrokenSchemeIsFatal(t *testing.T) {
	configs := verifyConfigsOnHost(t)
	user := t.TempDir()
	writeFile(t, filepath.Join(user, "broken.hrc"), `<?xml version="1.0" encoding="UTF-8"?>
<hrc version="take5" xmlns="http://colorer.sf.net/2003/hrc">
  <prototype name="brokentest" group="user" description="Broken">
    <location link="broken-type.hrc"/>
    <filename>/\.brokentest$/</filename>
  </prototype>
</hrc>
`)
	writeFile(t, filepath.Join(user, "broken-type.hrc"), `<hrc><type name="brokentest">`)
	// The file alone is named, so broken-type.hrc is read only when the type
	// is loaded, not as a prototype file.
	s, err := NewSession(context.Background(), "/base/catalog.xml", configs, WithUserHRC(filepath.Join(user, "broken.hrc")))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	_, err = s.LoadFileType("brokentest")
	var fe *FatalError
	if !errors.As(err, &fe) || fe.Op != "colorer_load_file_type" {
		t.Fatalf("got %v, want a *FatalError from colorer_load_file_type", err)
	}
}
