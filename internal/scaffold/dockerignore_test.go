package scaffold

import (
	"slices"
	"strings"
	"testing"
)

// BuildKit reads <Dockerfile>.dockerignore beside the Dockerfile and otherwise
// the context root's .dockerignore, which belongs to the project and may well
// exclude .slate itself; a .slate/.dockerignore is read by nothing.
func TestScaffoldsWriteADockerfileScopedIgnoreFile(t *testing.T) {
	for _, name := range []string{"laravel", "nextjs"} {
		s, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		got := s.(embeddedScaffold).FileMap("/p/.slate")["dockerignore.tmpl"]
		if got != "/p/.slate/Dockerfile.dockerignore" {
			t.Errorf("%s writes its ignore file to %q, want /p/.slate/Dockerfile.dockerignore", name, got)
		}
	}
}

// .slate holds the staged file mounts (composer's auth.json among them) and the
// per-workspace composer and npm caches, so the ignore file keeps it out of the
// build context except for the paths the Dockerfile copies.
func TestScaffoldIgnoreFilesAdmitOnlyTheSlatePathsTheDockerfileCopies(t *testing.T) {
	for _, name := range []string{"laravel", "nextjs"} {
		s, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		fsys := s.(embeddedScaffold).FS()
		ignore, err := fsys.ReadFile(name + "/dockerignore.tmpl")
		if err != nil {
			t.Fatal(err)
		}
		dockerfile, err := fsys.ReadFile(name + "/Dockerfile.tmpl")
		if err != nil {
			t.Fatal(err)
		}

		lines := strings.Split(strings.TrimSpace(string(ignore)), "\n")
		if !slices.Contains(lines, ".slate/*") {
			t.Errorf("%s ignore file does not exclude .slate/*", name)
		}

		var copied, admitted []string
		for _, line := range strings.Split(string(dockerfile), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 || fields[0] != "COPY" {
				continue
			}
			for _, src := range fields[1 : len(fields)-1] {
				if strings.HasPrefix(src, ".slate/") {
					copied = append(copied, src)
				}
			}
		}
		for _, line := range lines {
			if strings.HasPrefix(line, "!.slate/") {
				admitted = append(admitted, strings.TrimPrefix(line, "!"))
			}
		}
		slices.Sort(copied)
		slices.Sort(admitted)
		if !slices.Equal(copied, admitted) {
			t.Errorf("%s ignore file admits %v under .slate, but the Dockerfile copies %v", name, admitted, copied)
		}
	}
}
