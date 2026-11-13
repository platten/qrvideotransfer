package main

import (
	"slices"
	"strings"
	"testing"
)

func TestGoTest(t *testing.T) {
	if got := goTest("noasm,nounsafe", "-race", "./..."); !slices.Equal(got, []string{"test", "-tags=noasm,nounsafe", "-race", "./..."}) {
		t.Fatalf("goTest with tags = %v", got)
	}
	if got := goTest("", "./..."); !slices.Equal(got, []string{"test", "./..."}) {
		t.Fatalf("goTest without tags = %v", got)
	}
}

func TestLintRuns(t *testing.T) {
	got := lintRuns("noasm,nounsafe", false)
	want := [][]string{{"run", "./..."}, {"run", "--build-tags=noasm,nounsafe", "./..."}}
	if len(got) != 2 || !slices.Equal(got[0], want[0]) || !slices.Equal(got[1], want[1]) {
		t.Fatalf("lintRuns with tags = %v, want %v", got, want)
	}
	if got := lintRuns("", true); len(got) != 1 || !slices.Equal(got[0], []string{"run", "--fix", "./..."}) {
		t.Fatalf("lintRuns without tags = %v", got)
	}
}

func TestVulncheckRuns(t *testing.T) {
	got := vulncheckRuns("noasm,nounsafe")
	if len(got) != 3 {
		t.Fatalf("vulncheckRuns = %v, want one per target system", got)
	}
	for i, goos := range []string{"linux", "darwin", "windows"} {
		if got[i].goos != goos || strings.Join(got[i].args, " ") != "-tags=noasm,nounsafe ./..." {
			t.Errorf("run %d = %+v", i, got[i])
		}
	}
	if got := vulncheckRuns(""); strings.Join(got[0].args, " ") != "./..." {
		t.Errorf("untagged run = %+v", got[0])
	}
	if toolNamed("govulncheck").pkg != "golang.org/x/vuln/cmd/govulncheck@v1.9.0" {
		t.Error("govulncheck is not pinned to v1.9.0")
	}
}

func TestTestCommand(t *testing.T) {
	cases := []struct {
		args          []string
		wantEnv, want string
	}{
		{nil, "CGO_ENABLED=0", "test -tags=noasm,nounsafe ./..."},
		{[]string{"-race"}, "CGO_ENABLED=1", "test -tags=noasm,nounsafe -race ./..."},
		{[]string{"-untagged"}, "CGO_ENABLED=0", "test ./..."},
		{[]string{"-long", "-short", "--", "-count=1"}, "CGO_ENABLED=0 QRVT_LONG_TESTS=1", "test -tags=noasm,nounsafe -short -count=1 ./..."},
	}
	for _, c := range cases {
		env, args := testCommand(c.args, "noasm,nounsafe")
		if strings.Join(env, " ") != c.wantEnv || strings.Join(args, " ") != c.want {
			t.Errorf("testCommand(%v) = %v, %v; want %s, %s", c.args, env, args, c.wantEnv, c.want)
		}
	}
}
