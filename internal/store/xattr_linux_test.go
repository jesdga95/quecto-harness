package store

import (
	"bytes"
	"encoding/binary"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOnlySELinuxLabelsAreAssignedByPolicy(t *testing.T) {
	for _, name := range []string{"security.selinux", "security.capability", "security.ima", "security.other", "user.notes"} {
		if got := systemAttr(name); got != (name == "security.selinux") {
			t.Errorf("System attribute %q = %t", name, got)
		}
	}
}

func TestCopyPreservesAnInheritedLinuxACLAtItsFinalName(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	// Linux stores a version followed by sorted tag, permission and ID entries.
	const owner, user, group, mask, other = 1, 2, 4, 16, 32
	const noID = ^uint32(0)
	entries := []struct {
		tag, perm uint16
		id        uint32
	}{
		{owner, 7, noID}, {user, 4, 1001}, {group, 5, noID}, {mask, 7, noID}, {other, 0, noID},
	}
	var acl bytes.Buffer
	if err := binary.Write(&acl, binary.LittleEndian, uint32(2)); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(&acl, binary.LittleEndian, entries); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Setxattr(dst, "system.posix_acl_default", acl.Bytes(), 0); err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			t.Skip("The test filesystem does not support POSIX ACLs")
		}
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "note"), "contents")
	c := carryWith(t, src, dst, filepath.Join(t.TempDir(), "carry"))
	if aclSize, err := syscall.Getxattr(filepath.Join(dst, "note"), "system.posix_acl_access", nil); err != nil || aclSize == 0 {
		t.Fatalf("Inherited ACL size = %d, %v", aclSize, err)
	}
	if !c.Complete() || c.Copied != 1 || len(leftTemps(t, dst)) != 0 {
		t.Fatalf("Copy = %+v, temporary files = %v", c, leftTemps(t, dst))
	}
}
