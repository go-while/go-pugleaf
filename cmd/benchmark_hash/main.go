package main

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"math/rand"
	"time"
)

// generateRandomMsgIDs generates n random message-ids in typical usenet format
func generateRandomMsgIDs(n int) []string {
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		guid := make([]byte, 24)
		for j := range guid {
			guid[j] = byte(33 + rnd.Intn(94)) // printable ASCII
		}
		host := fmt.Sprintf("randomhost%d.net", rnd.Intn(10000))
		ids[i] = fmt.Sprintf("<%s@%s>", string(guid), host)
	}
	return ids
}

func main() {
	msgIDs := generateRandomMsgIDs(1000000)

	fmt.Printf("Benchmarking hash functions on %d random message-ids...\n", len(msgIDs))
	t0 := time.Now()
	for _, s := range msgIDs {
		h := md5.Sum([]byte(s))
		_ = hex.EncodeToString(h[:])
	}
	t1 := time.Since(t0)
	fmt.Printf("MD5:    %v\n", t1)

	t0 = time.Now()
	for _, s := range msgIDs {
		h := sha1.Sum([]byte(s))
		_ = hex.EncodeToString(h[:])
	}
	t1 = time.Since(t0)
	fmt.Printf("SHA1:   %v\n", t1)

	t0 = time.Now()
	for _, s := range msgIDs {
		h := sha256.Sum256([]byte(s))
		_ = hex.EncodeToString(h[:])
	}
	t1 = time.Since(t0)
	fmt.Printf("SHA256: %v\n", t1)

	t0 = time.Now()
	for _, s := range msgIDs {
		h := sha512.Sum512([]byte(s))
		_ = hex.EncodeToString(h[:])
	}
	t1 = time.Since(t0)
	fmt.Printf("SHA512: %v\n", t1)
}
