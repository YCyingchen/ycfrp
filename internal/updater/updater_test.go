package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"s2609.003", "s2609.002", 1},
		{"s2609.002", "s2609.003", -1},
		{"s2609.003", "s2609.003", 0},
		{"s2609.010", "s2609.009", 1},
		{"s2610.001", "s2609.099", 1},
		// 修订号位数不同时必须按数值比较，而不是字符串比较。
		{"s2609.30", "s2609.100", -1},
	}
	for _, c := range cases {
		got := CompareVersions(c.a, c.b)
		if (got > 0) != (c.want > 0) || (got < 0) != (c.want < 0) {
			t.Errorf("CompareVersions(%q,%q)=%d, want 方向 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseVersionRejectsGarbage(t *testing.T) {
	for _, v := range []string{"", "abc", "2609", "s.", "s2609.", "s.003"} {
		if _, ok := parseVersion(v); ok {
			t.Errorf("parseVersion(%q) 应当解析失败", v)
		}
	}
}

func TestManifestURLAndSameOrigin(t *testing.T) {
	got := ManifestURL("https://ycfrp.yc1.cc.cd/")
	if got != "https://ycfrp.yc1.cc.cd/version.json" {
		t.Errorf("ManifestURL=%q", got)
	}
	if !SameOrigin("https://ycfrp.yc1.cc.cd", "https://ycfrp.yc1.cc.cd/ycfrp-s2609.003-linux-amd64.tar.gz") {
		t.Error("同源地址应判定为 true")
	}
	if SameOrigin("https://ycfrp.yc1.cc.cd", "https://evil.example.com/x.tar.gz") {
		t.Error("异源地址必须被拒绝")
	}
}

func TestPackageNameStripsPath(t *testing.T) {
	got := packageName("https://ycfrp.yc1.cc.cd/ycfrp-s2609.003-linux-amd64.tar.gz?v=1")
	if got != "ycfrp-s2609.003-linux-amd64.tar.gz" {
		t.Errorf("packageName=%q", got)
	}
	// 无论输入什么，结果都只能是纯文件名，不能带路径分隔符或穿越片段。
	for _, in := range []string{
		"https://x/../../etc/passwd",
		"/../../etc/passwd",
		"..\\..\\windows\\system32\\cmd.exe",
		"",
	} {
		name := packageName(in)
		if filepath.Base(name) != name || name == "." || name == ".." {
			t.Errorf("packageName(%q)=%q 未清洗成纯文件名", in, name)
		}
	}
}

func TestExtractFromTarGzPicksYcfrp(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "pkg.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	entries := map[string][]byte{
		"README-linux.txt": []byte("说明"),
		"start.sh":         []byte("#!/bin/sh\n"),
		"ycfrp":            []byte("ELF-BINARY-CONTENT"),
	}
	for name, data := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	f.Close()

	out := filepath.Join(dir, "ycfrp.new")
	if _, err := ExtractBinary(archive, out, "ycfrp"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ELF-BINARY-CONTENT" {
		t.Errorf("解出的内容不正确：%q", got)
	}
}

func TestExtractFromZipPicksExe(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "pkg.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	files := map[string]string{
		"README-windows.txt": "说明",
		"YCFRP-console.exe":  "CONSOLE-EXE",
		"YCFRP.exe":          "GUI-EXE",
	}
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	zw.Close()
	f.Close()

	out := filepath.Join(dir, "app.new")
	if _, err := ExtractBinary(archive, out, "YCFRP.exe"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "GUI-EXE" {
		t.Errorf("应按 preferName 选中 GUI 版，实际 %q", got)
	}
}

func TestVerifyBinaryRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.bin")
	if err := os.WriteFile(bad, []byte("this is definitely not an executable file, padded out enough"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBinary(bad); err == nil {
		t.Error("非可执行文件必须被拒绝")
	}
}
