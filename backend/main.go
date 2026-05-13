//go:build wasip1

package main

import plugin "github.com/Paca-AI/plugin-sdk-go"

func init() {
	plugin.Run(&githubPlugin{})
}

func main() {}
