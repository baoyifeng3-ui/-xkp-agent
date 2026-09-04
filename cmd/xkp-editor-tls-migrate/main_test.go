package main

import (
	"github.com/docker/docker/api/types"
	"testing"
)

func TestMounted(t *testing.T) {
	mounts := []types.MountPoint{{Destination: "/root/.config/code-cert.pem"}}
	if !mounted(mounts, "/root/.config/code-cert.pem") || mounted(mounts, "/root/.config/code-cert-key.pem") {
		t.Fatal("mount detection failed")
	}
}
