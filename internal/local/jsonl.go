package local

import (
	"bufio"
	"os"
)

const maxJSONLLineBytes = 64 * 1024 * 1024

func ReadJSONLLines(path string, fn func(line []byte)) error {
	return readJSONLLines(path, maxJSONLLineBytes, fn)
}

func readJSONLLines(path string, maxLineBytes int, fn func(line []byte)) error {
	file, err := os.Open(path) // #nosec G304 -- Scriba intentionally reads user-configured local usage log paths.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, min(maxLineBytes, 1024*1024))
	scanner.Buffer(buf, maxLineBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		fn(line)
	}
	return scanner.Err()
}
