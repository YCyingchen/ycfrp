package api

import (
	"fmt"
	"io/fs"
	"runtime"

	"github.com/ycfrp/ycfrp/internal/webui"
)

// assets wraps the embedded web panel bundle.
type assets struct {
	fs fs.FS
}

func newAssets() (*assets, error) {
	root := webui.FS()
	if _, err := fs.Stat(root, "index.html"); err != nil {
		return nil, fmt.Errorf("面板资源未嵌入: %w", err)
	}
	return &assets{fs: root}, nil
}

func (a *assets) read(name string) ([]byte, error) {
	return fs.ReadFile(a.fs, name)
}

func runtimeVersion() string { return runtime.Version() }
