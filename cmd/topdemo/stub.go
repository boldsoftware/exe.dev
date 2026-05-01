// Package main is the topdemo manual-test program. The real entrypoint
// lives in main.go behind the `topdemo` build tag; this stub keeps the
// package buildable in the default build (without the tag).
//
//go:build !topdemo

package main

func main() {}
