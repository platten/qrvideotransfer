package payload

import (
	"slices"
	"testing"
)

// TestFormatCharactersInNames checks that names with Unicode format characters (category Cf, such as
// the zero-width joiner) are extracted, not refused, and that the caller gets every extracted path in
// manifest order, so that it can warn about such names.
func TestFormatCharactersInNames(t *testing.T) {
	name := "family‍\U0001F468.txt"
	stream, _ := pack(t, PackOptions{Inputs: tree{"d/": "", "d/" + name: "x"}.write(t, t.TempDir())})
	out := t.TempDir()
	res, err := unpack(stream, UnpackOptions{OutputDir: out})
	must(t, err)
	if want := []string{"d", "d/" + name}; !slices.Equal(res.Paths, want) {
		t.Fatalf("Paths = %q, want %q", res.Paths, want)
	}
	if got := read(t, out); got["d/"+name] != "x" {
		t.Fatalf("unpacked %v", got)
	}
}
