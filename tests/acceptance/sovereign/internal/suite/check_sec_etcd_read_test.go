//go:build unix

package suite

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// etcdFileVersions hands each opening of the etcd file of a fixture the next
// of versions, and the last one from then on. It stands in for the etcd file
// of a running server: etcd writes the file while the probe reads it, and a
// page that holds a stale copy of a record is written over at any time, so
// two readings of the file need not hold the same copies.
//
// The file is a symbolic link to named pipe i. When a reader opens pipe i,
// the link is moved to pipe i+1 before version i is written into pipe i, so
// the reader cannot reach the end of version i before the next opening of
// the file finds pipe i+1.
func etcdFileVersions(t *testing.T, root string, versions ...[]byte) {
	path := filepath.Join(root, filepath.FromSlash("var/lib/rancher/k3s/server/db/etcd/member/snap/db"))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	pipe := func(i int) string { return fmt.Sprintf("%s.pipe-%d", path, i) }
	point := func(i int) error { // links the file to pipe i
		if err := syscall.Mkfifo(pipe(i), 0o600); err != nil {
			return err
		}
		if err := os.Symlink(pipe(i), path+".link"); err != nil {
			return err
		}
		return os.Rename(path+".link", path)
	}
	if err := point(0); err != nil {
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			w, err := os.OpenFile(pipe(i), os.O_WRONLY, 0) // waits until a reader opens the file
			if err != nil {
				t.Error(err)
				return
			}
			select {
			case <-stop:
				_ = w.Close()
				return
			default:
			}
			if err := point(i + 1); err != nil {
				t.Error(err)
			}
			_, _ = w.Write(versions[min(i, len(versions)-1)])
			_ = w.Close()
		}
	}()
	t.Cleanup(func() {
		close(stop)
		// A reader that does not wait releases the writer that waits for one.
		if r, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			<-done
			_ = r.Close()
		}
	})
}

// TestSecEtcdReadsTheFileOnce: the probe judges the records of the canary
// Secret in one reading of the etcd file. On the local pack (region b, 18
// November 2026) the probe read the file three times, once per count: the
// second reading held two copies of the record, the live one and a stale
// copy in a page not yet written over, and the third held one. Both copies
// were secretbox ciphertext, and the check failed with "1 of 2 records ...
// are not secretbox ciphertext". A copy that is not secretbox ciphertext
// must still fail the check.
func TestSecEtcdReadsTheFileOnce(t *testing.T) {
	sealed := cipher("k8s:enc:secretbox:v1:key1:")
	for name, c := range map[string]struct {
		second []byte // the value of the second copy of the record
		want   string
	}{
		"a stale secretbox copy is written over between two readings": {sealed, ""},
		"one copy is not secretbox ciphertext":                        {cipher("k8s:enc:aescbc:v1:key1:"), "1 of 2 records of the canary Secret in the etcd file are not secretbox ciphertext"},
	} {
		t.Run(name, func(t *testing.T) {
			host := func(t *testing.T, root string, args []string) {
				etcdHost("secretbox")(t, root, args)
				one, err := os.ReadFile(filepath.Join(root, filepath.FromSlash("var/lib/rancher/k3s/server/db/etcd/member/snap/db")))
				if err != nil {
					t.Fatal(err)
				}
				// The record again, in a page that a later write of etcd reuses.
				two := append(append(append([]byte{}, one...), etcdRecord(args[0], c.second)...), make([]byte, 64)...)
				etcdFileVersions(t, root, two, two, one)
			}
			f := &probeFake{t: t, nodes: "cp-1 cp-2", host: host}
			r := probeRun(t, secEtcdCheck{kubectl: f.kubectl}, f, c.want)
			if ev := r.run.Steps[1].Evidence; c.want == "" && (ev["db_key"] != 2 || ev["db_key_secretbox"] != 2) {
				t.Errorf("want both copies of the record counted in one reading: %v", ev)
			}
		})
	}
}
