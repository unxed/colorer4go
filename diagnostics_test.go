package colorer

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyConfigs copies the repository's colorer/configs into a temporary
// directory, so a test can break one file without touching the real tree.
func copyConfigs(t *testing.T) string {
	t.Helper()
	src := verifyConfigsOnHost(t)
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copying configs: %v", err)
	}
	return dst
}

// editConfig rewrites one file under a copied configs tree.
func editConfig(t *testing.T, root, rel string, edit func(string) string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(edit(string(data))), 0o644); err != nil {
		t.Fatalf("writing %s: %v", rel, err)
	}
}

type diagnosticRecorder struct {
	got []Diagnostic
}

func (r *diagnosticRecorder) handle(d Diagnostic) { r.got = append(r.got, d) }

// find returns the first diagnostic at level whose message contains substr.
func (r *diagnosticRecorder) find(level Level, substr string) (Diagnostic, bool) {
	for _, d := range r.got {
		if d.Level == level && strings.Contains(d.Message, substr) {
			return d, true
		}
	}
	return Diagnostic{}, false
}

func (r *diagnosticRecorder) dump(t *testing.T) {
	t.Helper()
	for _, d := range r.got {
		t.Logf("  %s", d)
	}
}

func asFatal(t *testing.T, err error) *FatalError {
	t.Helper()
	var fe *FatalError
	if !errors.As(err, &fe) {
		t.Fatalf("got error %v (%T), want a *FatalError", err, err)
	}
	return fe
}

// The throw sites are checked by file only: line numbers move with every
// update of the Colorer sources, the file an exception comes from does not.
func assertThrowSite(t *testing.T, fe *FatalError, file string) {
	t.Helper()
	if !strings.HasPrefix(fe.Reason, "C++ exception thrown at "+file+":") {
		t.Errorf("Reason = %q, want the throw site in %s", fe.Reason, file)
	}
	if !strings.Contains(fe.Error(), fe.Reason) {
		t.Errorf("Error() = %q does not include the reason", fe.Error())
	}
}

func TestDiagnostics_MissingCatalogReportsFileAndThrowSite(t *testing.T) {
	configs := verifyConfigsOnHost(t)
	var rec diagnosticRecorder

	_, err := NewSession(context.Background(), "/base/no-such-catalog.xml", configs, WithDiagnostics(LevelWarn, rec.handle))
	if err == nil {
		t.Fatal("NewSession succeeded with a catalog that does not exist")
	}
	fe := asFatal(t, err)
	if fe.Op != "colorer_init" {
		t.Errorf("Op = %q, want colorer_init", fe.Op)
	}
	assertThrowSite(t, fe, "colorer/xml/libxml2/LibXmlInputSource.cpp")

	d, ok := rec.find(LevelError, "/base/no-such-catalog.xml")
	if !ok {
		rec.dump(t)
		t.Fatal("no error diagnostic names the missing catalog")
	}
	if !strings.HasPrefix(d.File, "colorer/") || d.Line <= 0 || d.Function == "" {
		t.Errorf("diagnostic %+v does not locate its Colorer source line", d)
	}
}

func TestDiagnostics_MalformedCatalogReportsXMLError(t *testing.T) {
	configs := copyConfigs(t)
	editConfig(t, configs, "base/catalog.xml", func(string) string { return "<catalog><hrc-sets>" })
	var rec diagnosticRecorder

	_, err := NewSession(context.Background(), "/base/catalog.xml", configs, WithDiagnostics(LevelWarn, rec.handle))
	assertThrowSite(t, asFatal(t, err), "colorer/parsers/CatalogParser.cpp")

	if _, ok := rec.find(LevelError, "/base/catalog.xml:1: parser error"); !ok {
		rec.dump(t)
		t.Fatal("libxml2's parse error for catalog.xml was not delivered")
	}
}

func TestDiagnostics_BrokenHRCFailsSelectTypeWithXMLError(t *testing.T) {
	configs := copyConfigs(t)
	editConfig(t, configs, "base/hrc/rare/json.hrc", func(s string) string { return s[:len(s)/2] })
	var rec diagnosticRecorder

	session, err := NewSession(context.Background(), "/base/catalog.xml", configs, WithDiagnostics(LevelWarn, rec.handle))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer session.Close()

	ok, err := session.SelectType("test.json", "{")
	if ok {
		t.Error("SelectType reported success for a truncated scheme")
	}
	fe := asFatal(t, err)
	if fe.Op != "colorer_select_type" {
		t.Errorf("Op = %q, want colorer_select_type", fe.Op)
	}
	assertThrowSite(t, fe, "colorer/parsers/HrcLibraryImpl.cpp")
	if _, ok := rec.find(LevelError, "/base/hrc/rare/json.hrc:"); !ok {
		rec.dump(t)
		t.Fatal("no error diagnostic points into json.hrc")
	}
}

// A regular expression that does not compile is something Colorer survives:
// it skips that one rule. Before diagnostics this was invisible.
func TestDiagnostics_BadRegexpIsReportedAndParsingContinues(t *testing.T) {
	configs := copyConfigs(t)
	editConfig(t, configs, "base/hrc/rare/json.hrc", func(s string) string {
		if !strings.Contains(s, "<regexp match=") {
			t.Fatal("json.hrc has no <regexp match= to break")
		}
		return strings.Replace(s, "<regexp match=", `<regexp match="/(((/" unused=`, 1)
	})
	var rec diagnosticRecorder

	session, err := NewSession(context.Background(), "/base/catalog.xml", configs, WithDiagnostics(LevelWarn, rec.handle))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer session.Close()
	if ok, err := session.SelectType("test.json", "{"); !ok || err != nil {
		t.Fatalf("SelectType = %v, %v", ok, err)
	}
	if _, err := session.ParseLine(`{"key": "value"}`); err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if err := session.Err(); err != nil {
		t.Errorf("Err() = %v after a recoverable problem", err)
	}
	if _, ok := rec.find(LevelError, "fault compiling regexp '/(((/'"); !ok {
		rec.dump(t)
		t.Fatal("the regexp that failed to compile was not reported")
	}
}

func TestDiagnostics_FailedCallStopsTheSession(t *testing.T) {
	verifyConfigsOnHost(t)
	session, err := NewSession(context.Background(), "/base/catalog.xml", "colorer/configs")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer session.Close()

	err = session.SetHRD("rgb", "no-such-scheme")
	fe := asFatal(t, err)
	if fe.Op != "colorer_set_hrd" {
		t.Errorf("Op = %q, want colorer_set_hrd", fe.Op)
	}
	assertThrowSite(t, fe, "colorer/parsers/ParserFactoryImpl.cpp")

	if got := session.Err(); got != error(fe) {
		t.Errorf("Err() = %v, want the SetHRD failure", got)
	}
	if ok, err := session.SelectType("test.json", "{"); ok || err != error(fe) {
		t.Errorf("SelectType after the failure = %v, %v; want false and the same error", ok, err)
	}
	if _, err := session.ParseLine("{}"); err != error(fe) {
		t.Errorf("ParseLine after the failure returned %v, want the same error", err)
	}
	if _, err := session.NextLine(); err != error(fe) {
		t.Errorf("NextLine after the failure returned %v, want the same error", err)
	}
	session.Reset() // must not panic or run into the module
}

func TestDiagnostics_LevelFiltersInsideColorer(t *testing.T) {
	verifyConfigsOnHost(t)
	var quiet, verbose diagnosticRecorder
	for _, c := range []struct {
		level Level
		rec   *diagnosticRecorder
	}{{LevelWarn, &quiet}, {LevelTrace, &verbose}} {
		session, err := NewSession(context.Background(), "/base/catalog.xml", "colorer/configs", WithDiagnostics(c.level, c.rec.handle))
		if err != nil {
			t.Fatalf("NewSession at %s: %v", c.level, err)
		}
		if ok, err := session.SelectType("test.json", "{"); !ok || err != nil {
			t.Fatalf("SelectType at %s = %v, %v", c.level, ok, err)
		}
		session.Close()
		for _, d := range c.rec.got {
			if d.Level > c.level {
				t.Errorf("handler at %s received %s", c.level, d)
			}
		}
	}
	if len(verbose.got) == 0 {
		t.Error("nothing was delivered at LevelTrace; is the logger registered at all?")
	}
	if len(quiet.got) != 0 {
		quiet.dump(t)
		t.Errorf("the stock catalog produced %d diagnostics at LevelWarn, want none", len(quiet.got))
	}
}

func TestDiagnostics_StreamWriterSplitsLines(t *testing.T) {
	var rec diagnosticRecorder
	host := &hostState{opts: sessionOptions{level: LevelInfo, handler: rec.handle}}
	w := &streamWriter{host: host, name: "stderr", level: LevelError}

	w.Write([]byte("first line\nsecond "))
	w.Write([]byte("line\r\n\npartial"))

	var msgs []string
	for _, d := range rec.got {
		if d.Level != LevelError || d.File != "stderr" {
			t.Errorf("unexpected diagnostic %+v", d)
		}
		msgs = append(msgs, d.Message)
	}
	if strings.Join(msgs, "|") != "first line|second line" {
		t.Errorf("delivered %q, want the two complete lines", msgs)
	}
	if got := host.reason(); got != "stderr: partial" {
		t.Errorf("reason() = %q, want the unterminated tail of stderr", got)
	}
}
