package webui

import (
	"embed"
	"io/fs"
)

//go:embed static
var embedded embed.FS

// FS returns the embedded panel assets rooted at the static directory.
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

// Read returns one embedded asset by name.
func Read(name string) ([]byte, error) {
	return fs.ReadFile(FS(), name)
}
