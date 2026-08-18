package identity

import "testing"

func TestBuildValidatesMachineIdentityIPAndMAC(t *testing.T) {
	if _, err := Build("", "host", "192.168.1.2", "00:11:22:33:44:55"); err == nil {
		t.Fatal("empty machine id accepted")
	}
	if _, err := Build("machine", "host", "999.1.1.1", "00:11:22:33:44:55"); err == nil {
		t.Fatal("invalid IP accepted")
	}
	if _, err := Build("machine", "host", "192.168.1.2", "not-a-mac"); err == nil {
		t.Fatal("invalid MAC accepted")
	}
	got, err := Build("machine\n", "host", "192.168.1.2", "00:11:22:33:44:55")
	if err != nil || len(got.MachineDigest) != 64 {
		t.Fatalf("valid identity failed: %#v %v", got, err)
	}
}
