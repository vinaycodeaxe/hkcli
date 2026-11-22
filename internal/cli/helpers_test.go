package cli

import "testing"

func TestRemoteFileNamesAreMadeSafe(t *testing.T) {
	cases := map[string]string{
		"../../evil.exe":   "evil.exe",
		`a\b\..\c.txt`:     "c.txt",
		"CON.txt":          "_CON.txt",
		"nul":              "_nul",
		"..":               "file",
		"re:port?.pdf":     "re_port_.pdf",
		"  .hidden. ":      "hidden",
		"lpt9.backup.json": "_lpt9.backup.json",
	}
	for remote, want := range cases {
		if got := safeFilename(remote); got != want {
			t.Errorf("safeFilename(%q) = %q, want %q", remote, got, want)
		}
	}
}
