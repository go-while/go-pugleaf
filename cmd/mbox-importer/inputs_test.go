package main

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type testMessage struct {
	head []string
	body []string
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// readAllMessages reads src to io.EOF, draining every body with readBody.
func readAllMessages(t *testing.T, src *source) []testMessage {
	t.Helper()
	var out []testMessage
	for i := 0; ; i++ {
		if i > 1000 {
			t.Fatal("parser not advancing")
		}
		msg, err := src.Stream.ReadMessage()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("ReadMessage: %v", err)
		}
		body, _ := readBody(msg.BodyReader(), true)
		out = append(out, testMessage{head: msg.Headers(), body: body})
	}
}

// messageIDs returns the message-id of every message of src.
func messageIDs(t *testing.T, src *source) []string {
	t.Helper()
	var ids []string
	for _, m := range readAllMessages(t, src) {
		id, r := messageIDFromHeaders(m.head, false)
		if r != skipNone {
			t.Fatalf("message without id: %v %q", r, m.head)
		}
		ids = append(ids, id)
	}
	return ids
}

var fixtureIDs = []string{"<first@example.invalid>", "<second@example.invalid>"}

func gzipBytes(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestMboxInExpandInputs(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.mbox", "a.mbox.gz", "c.bag", "x.mbox.done", "readme.txt", "d.mbox.7z"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.mbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub.mbox", "inner.mbox"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	single := filepath.Join(dir, "readme.txt") // a file argument is taken as given
	missing := filepath.Join(dir, "missing.mbox")

	files, errs := expandInputs([]string{dir, missing, single})
	want := []string{
		filepath.Join(dir, "a.mbox.gz"),
		filepath.Join(dir, "b.mbox"),
		filepath.Join(dir, "c.bag"),
		filepath.Join(dir, "d.mbox.7z"),
		single,
	}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("files:\n got %q\nwant %q", files, want)
	}
	if len(errs) != 1 || !errors.Is(errs[0], os.ErrNotExist) || !strings.Contains(errs[0].Error(), "missing.mbox") {
		t.Errorf("errs: %v", errs)
	}
}

func TestMboxInOpenPlain(t *testing.T) {
	for _, name := range []string{"group.mbox", "group.bag", "group.mbox.done"} {
		src, err := openSource(writeFile(t, name, fixtureMbox), "7z")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := messageIDs(t, src); !reflect.DeepEqual(got, fixtureIDs) {
			t.Errorf("%s: ids %q", name, got)
		}
		if src.Unread() {
			t.Errorf("%s: Unread after a full read", name)
		}
		if err := src.Close(); err != nil {
			t.Errorf("%s: Close: %v", name, err)
		}
		if err := src.Close(); err != nil {
			t.Errorf("%s: second Close: %v", name, err)
		}
	}
}

func TestMboxInOpenGzipTwoMembers(t *testing.T) {
	// one gzip member per article, concatenated (what `cat a.gz b.gz` gives)
	split := strings.Index(fixtureMbox[1:], "From nobody") + 1
	data := append(gzipBytes(t, fixtureMbox[:split]), gzipBytes(t, fixtureMbox[split:])...)
	path := writeFile(t, "group.mbox.gz", string(data))
	src, err := openSource(path, "7z")
	if err != nil {
		t.Fatal(err)
	}
	if got := messageIDs(t, src); !reflect.DeepEqual(got, fixtureIDs) {
		t.Errorf("ids %q", got)
	}
	if src.Unread() {
		t.Error("Unread after a full read")
	}
	if err := src.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func writeZip(t *testing.T, name string, members map[string]string, order []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, m := range order {
		w, err := zw.Create(m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(members[m])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMboxInOpenZip(t *testing.T) {
	members := map[string]string{"readme.txt": "not an mbox\n", "dir/group.mbox": fixtureMbox, "data": fixtureMbox}
	for _, tc := range []struct {
		name  string
		order []string
		ok    bool
	}{
		{"named member after another", []string{"readme.txt", "dir/group.mbox"}, true},
		{"only member, any name", []string{"data"}, true},
		{"two members, none named", []string{"readme.txt", "data"}, false},
	} {
		path := writeZip(t, "group.mbox.zip", members, tc.order)
		src, err := openSource(path, "7z")
		if !tc.ok {
			if err == nil {
				_ = src.Close()
				t.Errorf("%s: no error", tc.name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := messageIDs(t, src); !reflect.DeepEqual(got, fixtureIDs) {
			t.Errorf("%s: ids %q", tc.name, got)
		}
		if src.Unread() {
			t.Errorf("%s: Unread after a full read", tc.name)
		}
		if err := src.Close(); err != nil {
			t.Errorf("%s: Close: %v", tc.name, err)
		}
	}
}

func TestMboxInOpenEmpty(t *testing.T) {
	for name, content := range map[string]string{
		"empty.mbox":    "",
		"empty.mbox.gz": string(gzipBytes(t, "")),
	} {
		src, err := openSource(writeFile(t, name, content), "7z")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if src.Stream != nil {
			t.Errorf("%s: Stream not nil", name)
		}
		if src.Unread() {
			t.Errorf("%s: Unread", name)
		}
		if err := src.Close(); err != nil {
			t.Errorf("%s: Close: %v", name, err)
		}
	}
	zpath := writeZip(t, "empty.mbox.zip", map[string]string{"x.mbox": ""}, []string{"x.mbox"})
	src, err := openSource(zpath, "7z")
	if err != nil || src.Stream != nil {
		t.Errorf("empty zip member: %v", err)
	}
	if src != nil {
		_ = src.Close()
	}
}

func TestMboxInOpenNotMbox(t *testing.T) {
	for name, content := range map[string]string{
		"text.mbox":      "Subject: no separator\n\nbody\n",
		"text.mbox.gz":   string(gzipBytes(t, "Subject: no separator\n\nbody\n")),
		"broken.mbox.gz": "this is not gzip data",
	} {
		src, err := openSource(writeFile(t, name, content), "7z")
		if err == nil {
			_ = src.Close()
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := openSource(filepath.Join(t.TempDir(), "missing.mbox"), "7z"); err == nil {
		t.Error("missing file: no error")
	}
}

func TestMboxInUnread(t *testing.T) {
	// abandoned after the first message, its body not read: the next ReadMessage
	// finds no separator and says io.EOF, but the input was not read to its end
	src, err := openSource(writeFile(t, "group.mbox", fixtureMbox), "7z")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Stream.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Stream.ReadMessage(); err != io.EOF {
		t.Fatalf("second ReadMessage without draining the body: %v, want io.EOF", err)
	}
	if !src.Unread() {
		t.Error("Unread false after the parser stopped early")
	}
	if err := src.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	// a large input (more than the 1 MiB buffer) abandoned after the first message
	var big strings.Builder
	for big.Len() < 3*inputBufferSize {
		big.WriteString(fixtureMbox)
	}
	src, err = openSource(writeFile(t, "big.mbox", big.String()), "7z")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := src.Stream.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	readBody(msg.BodyReader(), false)
	if !src.Unread() {
		t.Error("Unread false on a stream abandoned after the first message")
	}
	if err := src.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestMboxInMarkDone(t *testing.T) {
	path := writeFile(t, "group.mbox", fixtureMbox)
	if err := markDone(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("original still there: %v", err)
	}
	if b, err := os.ReadFile(path + ".done"); err != nil || string(b) != fixtureMbox {
		t.Errorf("done file: %v", err)
	}
	if err := markDone(path); err == nil {
		t.Error("markDone of a missing file: no error")
	}

	// an existing .done file is never replaced
	dir := t.TempDir()
	src := filepath.Join(dir, "a.mbox")
	if err := os.WriteFile(src, []byte("NEW"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src+".done", []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := markDone(src); !errors.Is(err, os.ErrExist) {
		t.Errorf("markDone over an existing .done: %v, want ErrExist", err)
	}
	for p, want := range map[string]string{src: "NEW", src + ".done": "OLD"} {
		if b, err := os.ReadFile(p); err != nil || string(b) != want {
			t.Errorf("%s: %q %v, want %q", p, b, err, want)
		}
	}
}

func TestMboxIn7zMissingProgram(t *testing.T) {
	path := writeFile(t, "group.mbox.7z", "not opened")
	src, err := openSource(path, filepath.Join(t.TempDir(), "no-such-7z"))
	if err == nil {
		_ = src.Close()
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "7z program") {
		t.Errorf("error %v", err)
	}
}

// need7z returns the 7z program or skips the test.
func need7z(t *testing.T) string {
	t.Helper()
	prog, err := exec.LookPath("7z")
	if err != nil {
		t.Skip("7z not installed")
	}
	return prog
}

// make7z creates archive (in dir) from the named files of dir with `7z a -bd`.
func make7z(t *testing.T, prog, dir, archive string, files ...string) string {
	t.Helper()
	args := append([]string{"a", "-bd", "--", archive}, files...)
	cmd := exec.Command(prog, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("7z a: %v\n%s", err, out)
	}
	return filepath.Join(dir, archive)
}

func TestMboxIn7z(t *testing.T) {
	prog := need7z(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "group.mbox"), []byte(fixtureMbox), 0o644); err != nil {
		t.Fatal(err)
	}
	// a second member that would end up in the stream (and in the last body) if the
	// command did not select the mbox member
	if err := os.WriteFile(filepath.Join(dir, "zz-readme.txt"), []byte("README TEXT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := make7z(t, prog, dir, "group.mbox.7z", "group.mbox", "zz-readme.txt")

	src, err := openSource(archive, "7z")
	if err != nil {
		t.Fatal(err)
	}
	msgs := readAllMessages(t, src)
	if len(msgs) != 2 || !reflect.DeepEqual(msgs[1].body, []string{"reply body"}) {
		t.Errorf("messages %d, last body %q", len(msgs), msgs[len(msgs)-1].body)
	}
	if src.Unread() {
		t.Error("Unread after a full read")
	}
	if err := src.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	// abandoned after the first message: Close kills 7z, reaps it, no error
	src, err = openSource(archive, prog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Stream.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if !src.Unread() {
		t.Error("Unread false on an abandoned 7z stream")
	}
	if err := src.Close(); err != nil {
		t.Errorf("Close of an abandoned 7z stream: %v", err)
	}
	if src.cmd.ProcessState == nil {
		t.Error("7z process not waited for")
	}

	// members in a subdirectory and with an upper-case suffix are found too
	for _, member := range []string{"sub/g.mbox", "G.MBOX"} {
		mdir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(mdir, filepath.Dir(member)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(mdir, member), []byte(fixtureMbox), 0o644); err != nil {
			t.Fatal(err)
		}
		archive := make7z(t, prog, mdir, "member.mbox.7z", member)
		src, err := openSource(archive, "7z")
		if err != nil {
			t.Fatalf("%s: %v", member, err)
		}
		if got := messageIDs(t, src); !reflect.DeepEqual(got, fixtureIDs) {
			t.Errorf("%s: ids %q", member, got)
		}
		if err := src.Close(); err != nil {
			t.Errorf("%s: Close: %v", member, err)
		}
	}
}

func TestMboxIn7zNoMboxMember(t *testing.T) {
	prog := need7z(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("From nobody Thu Dec  8 13:43:59 2016\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := make7z(t, prog, dir, "group.mbox.7z", "readme.txt")
	src, err := openSource(archive, "7z")
	if err == nil {
		_ = src.Close()
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "no *.mbox or *.bag member") {
		t.Errorf("error %v", err)
	}

	// not a 7z archive at all: 7z fails, the error says so
	bad := writeFile(t, "bad.mbox.7z", "this is not a 7z archive")
	src, err = openSource(bad, "7z")
	if err == nil {
		_ = src.Close()
		t.Fatal("not an archive: no error")
	}
	if !strings.Contains(err.Error(), "7z") {
		t.Errorf("not an archive: error %v", err)
	}
}
