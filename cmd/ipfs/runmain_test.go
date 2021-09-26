// +build testrunmain

package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"testing"
	"time"
)

// this abuses go so much that I felt dirty writing this code
// but it is the only way to do it without writing custom compiler that would
// be a clone of go-build with go-test
func TestRunMain(t *testing.T) {
	args := flag.Args()
	os.Args = append([]string{os.Args[0]}, args...)
	ret := mainRet()

	p := os.Getenv("IPFS_COVER_RET_FILE")
	if len(p) != 0 {
		ioutil.WriteFile(p, []byte(fmt.Sprintf("%d\n", ret)), 0777)
	}

	// close outputs so go testing doesn't print anything
	null, _ := os.Open(os.DevNull)
	os.Stderr = null
	os.Stdout = null
}

func TestTime1(t *testing.T) {
	time.se
	now := time.Now()
	d1 := time.Now().UnixNano()
	t.Log(now.Format("2006/01/02 15:04:05.999999999"))
	t.Log("unixNano:", d1)

	t1 := time.Unix(0, d1)
	t.Log(t1.Format("2006/01/02 15:04:05.999999999"))
}
