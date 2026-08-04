package webull_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/momentum-intelligence-platform/mip/internal/webull"
)

func TestTokenFileRoundTripUsesOwnerOnlyPermissions(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "private", "token.txt")
	want := webull.AccessToken{
		Token: "sensitive-token", Expires: 1786552668433, Status: "NORMAL",
	}
	if err := webull.WriteTokenFile(path, want); err != nil {
		t.Fatalf("WriteTokenFile() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	got, err := webull.ReadTokenFile(path)
	if err != nil {
		t.Fatalf("ReadTokenFile() error = %v", err)
	}
	if got != want {
		t.Fatalf("token = %+v", got)
	}
}
