package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGatewayBinary_resolution(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	self := filepath.Join(dir, "forjad-self")
	configured := filepath.Join(dir, "forjad-configured")
	for _, p := range []string{self, configured} {
		if err := os.WriteFile(p, []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	executable := func() (string, error) { return self, nil }
	tests := []struct {
		name       string
		configured string
		goos       string
		want       string
		wantErr    error
	}{
		{name: "configured wins on linux", configured: configured, goos: "linux", want: configured},
		{name: "configured wins elsewhere", configured: configured, goos: "darwin", want: configured},
		{name: "unset on linux is this executable", goos: "linux", want: self},
		{name: "unset elsewhere is refused", goos: "darwin", wantErr: ErrNoGatewayBinary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveGatewayBinary(tt.configured, tt.goos, executable)
			if !errors.Is(err, tt.wantErr) || got != tt.want {
				t.Fatalf("resolveGatewayBinary = %q, %v; want %q, %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestGatewayBinary_missingFile_errorNamesIt(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "no-forjad")
	_, err := resolveGatewayBinary(missing, "linux", os.Executable)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resolveGatewayBinary(missing) = %v, want not exist", err)
	}
}

func TestValidRunID(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"0123456789abcdef0123456789abcdef", "test-00ff", "a"} {
		if err := validRunID(id); err != nil {
			t.Errorf("validRunID(%q) = %v, want nil", id, err)
		}
	}
	for _, id := range []string{"", "-lead", "has space", "a/b", "../x", string(make([]byte, 65))} {
		if err := validRunID(id); err == nil {
			t.Errorf("validRunID(%q) = nil, want it refused: it names a network and containers", id)
		}
	}
}
