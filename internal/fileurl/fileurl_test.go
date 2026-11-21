package fileurl

import "testing"

func TestPathFor(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		windows bool
		want    string
		wantErr bool
	}{
		// Windows drive letters.
		{"windows drive letter", "file:///C:/config.json", true, `C:\config.json`, false},
		{"windows lowercase drive", "file:///d:/x/y.json", true, `d:\x\y.json`, false},
		{"windows localhost host", "file://localhost/C:/x.json", true, `C:\x.json`, false},
		{"windows UNC path", "file://server/share/x.json", true, `\\server\share\x.json`, false},
		// Percent-decoded names.
		{"windows space", "file:///C:/My%20Videos/a%20b.json", true, `C:\My Videos\a b.json`, false},
		{"windows chinese", "file:///D:/%E4%B8%AD%E6%96%87/%E5%BD%B1%E7%89%87.json", true, `D:\中文\影片.json`, false},
		{"unix space", "file:///home/My%20Videos/x.json", false, "/home/My Videos/x.json", false},
		{"unix chinese", "file:///home/%E4%B8%AD%E6%96%87/x.json", false, "/home/中文/x.json", false},
		// Plain paths, unchanged.
		{"unix absolute", "file:///home/user/config.json", false, "/home/user/config.json", false},
		{"unix localhost host", "file://localhost/home/x.json", false, "/home/x.json", false},
		// Errors.
		{"not a file URL", "https://example.com/x.json", true, "", true},
		{"not a file URL unix", "https://example.com/x.json", false, "", true},
		{"invalid escape", "file:///C:/bad%zz.json", true, "", true},
		{"empty", "", true, "", true},
		{"unix remote host", "file://server/share/x.json", false, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := PathFor(c.raw, c.windows)
			if c.wantErr {
				if err == nil {
					t.Fatalf("PathFor(%q, %v) = %q, want error", c.raw, c.windows, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("PathFor(%q, %v) = %q, %v; want %q", c.raw, c.windows, got, err, c.want)
			}
			if got != c.want {
				t.Fatalf("PathFor(%q, %v) = %q, want %q", c.raw, c.windows, got, c.want)
			}
		})
	}
}

// Path uses the current OS; on Linux it behaves like PathFor with
// windows=false.
func TestPathOnLinux(t *testing.T) {
	got, err := Path("file:///home/My%20Videos/x.json")
	if err != nil {
		t.Fatalf("Path = %q, %v", got, err)
	}
	if got != "/home/My Videos/x.json" {
		t.Fatalf("Path = %q, want %q", got, "/home/My Videos/x.json")
	}
}
