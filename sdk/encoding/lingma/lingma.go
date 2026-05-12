package lingma

import (
	"encoding/base64"
	"fmt"
)

const (
	CustomAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
	StdAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	CustomPad      = '$'
	StdPad         = '='
)

var (
	s2c [256]byte
	c2s [256]byte
)

func init() {
	for i := 0; i < 256; i++ {
		s2c[i] = 0
		c2s[i] = 0
	}
	for i := 0; i < 64; i++ {
		s2c[StdAlphabet[i]] = CustomAlphabet[i]
		c2s[CustomAlphabet[i]] = StdAlphabet[i]
	}
	s2c[StdPad] = CustomPad
	c2s[CustomPad] = StdPad
}

// Encode converts plaintext to Lingma encoded string using a zero-allocation map.
func Encode(plaintext []byte) string {
	std := base64.StdEncoding.EncodeToString(plaintext)
	n := len(std)
	if n == 0 {
		return ""
	}
	a := n / 3

	encoded := make([]byte, n)
	
	// Part 1: std[n-a:n] -> encoded[0:a]
	for i := 0; i < a; i++ {
		encoded[i] = s2c[std[n-a+i]]
	}
	// Part 2: std[a:n-a] -> encoded[a:n-a]
	for i := a; i < n-a; i++ {
		encoded[i] = s2c[std[i]]
	}
	// Part 3: std[0:a] -> encoded[n-a:n]
	for i := 0; i < a; i++ {
		encoded[n-a+i] = s2c[std[i]]
	}

	return string(encoded)
}

// Decode converts a Lingma encoded string back to plaintext.
func Decode(encoded string) ([]byte, error) {
	n := len(encoded)
	if n == 0 {
		return []byte{}, nil
	}

	a := n / 3
	std := make([]byte, n)
	
	// Inverse mapping
	// encoded[0:a] corresponds to std[n-a:n]
	for i := 0; i < a; i++ {
		char := encoded[i]
		val := c2s[char]
		if val == 0 && char != CustomAlphabet[0] {
			return nil, fmt.Errorf("char out of custom alphabet: %c", char)
		}
		std[n-a+i] = val
	}
	// encoded[a:n-a] corresponds to std[a:n-a]
	for i := a; i < n-a; i++ {
		char := encoded[i]
		val := c2s[char]
		if val == 0 && char != CustomAlphabet[0] {
			return nil, fmt.Errorf("char out of custom alphabet: %c", char)
		}
		std[i] = val
	}
	// encoded[n-a:n] corresponds to std[0:a]
	for i := 0; i < a; i++ {
		char := encoded[n-a+i]
		val := c2s[char]
		if val == 0 && char != CustomAlphabet[0] {
			return nil, fmt.Errorf("char out of custom alphabet: %c", char)
		}
		std[i] = val
	}

	return base64.StdEncoding.DecodeString(string(std))
}
