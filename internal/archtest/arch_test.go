// Package archtest enforces the layering rules from AGENTS.md: which
// packages may import which. Breaking a rule fails the build in CI instead of
// relying on reviewers to notice.
package archtest

import (
	"bufio"
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

const module = "go.xchunk.org/vela"

// rule forbids packages matching from from importing anything listed in
// deny (module-relative prefixes, or standard library paths).
type rule struct {
	from   string
	deny   []string
	reason string
}

var rules = []rule{
	{"internal/content", []string{"internal/"}, "the blob contract is backend independent"},
	{"internal/blobtest", []string{"internal/service", "internal/store", "internal/server"}, "contract tests know only blob interfaces"},
	{"api", []string{"internal/", "client"}, "the public contract must stand alone"},
	{"client", []string{"internal/"}, "the public client is importable by other modules"},
	{"internal/domain", []string{"internal/"}, "entities depend on nothing but the contract"},
	{"internal/service", []string{"internal/store", "internal/blob", "internal/server", "internal/wire", "internal/cli",
		"std:net/http", "std:database/sql"}, "services know neither HTTP nor SQL nor the filesystem layout"},
	{"internal/store", []string{"internal/service", "internal/server", "internal/blob", "internal/cli"}, "storage is below the service"},
	{"internal/blob", []string{"internal/service", "internal/server", "internal/store", "internal/cli"}, "storage is below the service"},
	{"internal/sign", []string{"internal/"}, "pure crypto"},
	{"internal/server", []string{"internal/store", "internal/blob", "internal/wire", "internal/cli", "std:database/sql"},
		"handlers go through the service"},
	{"internal/index", []string{"internal/service", "internal/store", "internal/server"}, "rendering only"},
	{"internal/cli", []string{"internal/service", "internal/store", "internal/blob", "internal/server", "internal/wire"},
		"the CLI talks to the server through the public client only"},
}

func TestLayering(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", module+"/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	checked := 0
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		pkg := strings.TrimPrefix(fields[0], module+"/")
		for _, r := range rules {
			if pkg != r.from && !strings.HasPrefix(pkg, r.from+"/") {
				continue
			}
			checked++
			for _, imp := range fields[1:] {
				for _, d := range r.deny {
					var bad bool
					if std, ok := strings.CutPrefix(d, "std:"); ok {
						bad = imp == std
					} else {
						rel, inModule := strings.CutPrefix(imp, module+"/")
						bad = inModule && (rel == strings.TrimSuffix(d, "/") || strings.HasPrefix(rel, d))
					}
					// A package may use its own sub-packages.
					if bad && !strings.HasPrefix(imp, module+"/"+r.from) {
						t.Errorf("%s imports %s: %s", pkg, imp, r.reason)
					}
				}
			}
		}
	}
	if checked < len(rules) {
		t.Fatalf("only %d packages matched %d rules; is the module path right?", checked, len(rules))
	}
}

func TestCLIDoesNotLinkS3(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", module+"/cmd/vela").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "github.com/aws/") {
		t.Fatal("S3 SDK linked into CLI")
	}
}
