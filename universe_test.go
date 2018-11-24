package virtuakube

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestUniverse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u, err := New(ctx)
	if err != nil {
		t.Fatalf("Creating universe: %s", err)
	}
	tmp, err := u.Tmpdir("test")
	if err != nil {
		t.Fatalf("Creating universe tmpdir: %s", err)
	}
	if !strings.HasPrefix(tmp, u.tmpdir) {
		t.Fatalf("tmpdir %q not inside universe tmpdir %q", tmp, u.tmpdir)
	}
	u.Close()
	_, err = os.Stat(tmp)
	if err == nil {
		t.Fatalf("tmpdir %q still exists after universe destruction", tmp)
	}
	if !os.IsNotExist(err) {
		t.Fatalf("unexpected error checking for tmpdir existence: %s", err)
	}
}
