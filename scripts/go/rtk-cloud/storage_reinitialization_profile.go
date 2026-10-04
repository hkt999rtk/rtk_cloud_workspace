package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
)

// Keep the parsed credentials and their audit hash bound to one private byte
// snapshot. Later path checks detect replacement without changing this snapshot.
func readStorageReinitializationProfile(path string) (map[string]string, string, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, "", errors.New("cannot inspect storage reinitialization profile")
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 {
		return nil, "", errors.New("storage reinitialization profile must be a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", errors.New("cannot open storage reinitialization profile")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o077 != 0 {
		return nil, "", errors.New("storage reinitialization profile changed while opening")
	}
	body, err := io.ReadAll(file)
	if err != nil {
		return nil, "", errors.New("cannot read storage reinitialization profile")
	}
	after, err := file.Stat()
	if err != nil || after.Mode().Perm()&0o077 != 0 || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return nil, "", errors.New("storage reinitialization profile changed while reading")
	}
	return parseEnvFileBytes(body), fmt.Sprintf("%x", sha256.Sum256(body)), nil
}
