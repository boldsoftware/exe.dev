package exelet

import (
	"embed"
	"io/fs"
)

//go:embed *
var Content embed.FS

// Get returns the specified file from the fs
func Get(name string) (fs.File, error) {
	return Content.Open(name)
}

// Kernel returns the exelet default kernel
func Kernel() (fs.File, error) {
	return Content.Open("kernel")
}

// ExeInit returns the exelet default exe-init
func ExeInit() (fs.File, error) {
	return Content.Open("exe-init")
}

// ExeSsh returns the exelet default exe-ssh
func ExeSsh() (fs.File, error) {
	return Content.Open("exe-ssh")
}
