package history

import (
	"os"
	"strconv"
	"time"
)

// Utility functions to replace go-utils dependencies

// UnixTimeSec returns current Unix timestamp in seconds
func UnixTimeSec() int64 {
	return time.Now().Unix()
}

// Str2int64 converts string to int64, returns 0 on error
func Str2int64(s string) int64 {
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return i
}

// DirExists checks if directory exists
func DirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// FileExists checks if file exists
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Mkdir creates directory and all parent directories
func Mkdir(path string) bool {
	err := os.MkdirAll(path, 0755)
	return err == nil
}
