package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransfersErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	config := write("config.json", "{}")
	request := write("request.json", `{"proposal": {"safeBlock": 24000000, "transaction": {"chainId": 1}}}`)

	tests := []struct {
		args   []string
		code   int
		stderr string
	}{
		{nil, 2, "-request-file is required"},
		{[]string{"-request-file", request, "-blocks", "0"}, 2, "-blocks must be at least 1"},
		{[]string{"-request-file", request, "-to", "0x1234"}, 2, `-to "0x1234"`},
		{[]string{"-request-file", request, "-in", "-from", "0x1234"}, 2, `-from "0x1234"`},
		{[]string{"-request-file", request, "-from", "0x000000000000000000000000000000000000dEaD"}, 2, "-from only applies to transfers into the Safe"},
		{[]string{"-request-file", request, "-in", "-to", "0x000000000000000000000000000000000000dEaD"}, 2, "-to only applies to transfers out of the Safe"},
		{[]string{"-request-file", request, "extra"}, 2, "Usage: arbot transfers"},
		{[]string{"-request-file", write("no-block.json", `{"proposal": {"transaction": {"chainId": 1}}}`)}, 1, "no Safe chain ID or block"},
		{[]string{"-request-file", write("no-chain.json", `{"proposal": {"safeBlock": 24000000}}`)}, 1, "no Safe chain ID or block"},
		{[]string{"-request-file", filepath.Join(dir, "missing.json")}, 1, "no such file"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		args := append([]string{"arbot", "-config", config, "transfers"}, test.args...)
		code := Run(t.Context(), args, &stdout, &stderr)
		if code != test.code || stdout.Len() != 0 || !strings.Contains(stderr.String(), test.stderr) {
			t.Errorf("transfers %v: got status %d, stdout %q, stderr %q; want %d, no output, and stderr containing %q",
				test.args, code, stdout.String(), stderr.String(), test.code, test.stderr)
		}
	}
}
